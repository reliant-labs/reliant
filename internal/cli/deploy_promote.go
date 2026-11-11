package cli

// The RELEASE half of `forge env deploy <env> [vX | --from <src-env>]` (ADR
// docs/adr/env-verbs.md, task V3). Was promote.go, reached as
// `forge env deploy <env> <version>`.
//
// WHAT THIS HALF DOES, AND WHAT IT DELIBERATELY DOES NOT. It reads the release
// `forge env build --release` cut, freezes each image's digest, and APPENDS one
// entry to the env's promotion ledger — the project's
// .forge/promotions/<env>.jsonl, or the control plane the env's KCL declares.
// No build runs; the bytes that were cut as <version> are, by construction,
// the bytes the env will run. It ships NOTHING: the apply and the health gate
// are the other half of the verb, in deploy_promote_follow.go.
//
// Keeping the ledger write separate from the apply is what makes the invariant
// "the bytes that passed staging ARE the bytes in prod" checkable — the
// digests are frozen the moment the env is promoted, independent of any later
// edit or move of the release file, and independent of whether the apply
// succeeded.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/reliant-labs/forge/pkg/deploystate"
	"github.com/reliant-labs/forge/pkg/release"
)

// promoteOptions carries the flags and the seams into runPromote.
//
// The three seams (Bindings, Releases, Git) are injected for the same reason
// env verify injects its three: a test asserting that --plan writes nothing,
// or that a backwards move is reported as one, should be able to STATE the ledger
// and the git history rather than staging a project and a repository to imply
// them. Production leaves them nil and gets the real ones.
type promoteOptions struct {
	// Claim, set only by `deploy --claimable`, resolves the document's `claim`
	// object once the deploy is live (its URL is not known before).
	Claim func() *deployClaimJSON
	// DryRun computes the plan and stops. Nothing is written, exit 0.
	DryRun bool
	// JSON switches the RENDERING only. The plan is computed before either
	// renderer runs, so the two modes cannot disagree about what was found.
	JSON bool
	// ProjectDir is the checkout read from. Empty falls back to discovery.
	ProjectDir string
	// Note and Actor are recorded on the ledger entry.
	Note  string
	Actor string
	// ExpectCurrent overrides the compare-and-set expectation the plan
	// read: a promotion id, or expectUnboundLiteral. Empty = the plan's.
	ExpectCurrent string
	// Supersede admits a promote while the current rollout is in flight.
	Supersede bool
	// AllowPassedMigrations admits a release carrying a migration the env's
	// schema has already passed (release_migrations.go).
	AllowPassedMigrations bool
	// RaiseFloorReason is `--floor "<why>"`: also set a roll-forward-only
	// rollback floor at the promoted release, in the same write (floor.go).
	RaiseFloorReason string

	// Follow is the apply-and-gate stage that runs after the ledger write
	// (deploy_promote_follow.go). NIL MEANS LEDGER-WRITE ONLY: nothing is
	// applied and nothing is waited on.
	//
	// `forge env deploy` always states it, so the verb always means
	// record + apply + wait. Nil is for the callers whose subject is the
	// ledger itself — the plan/CAS/gates/provenance tests, and
	// `forge release`'s own fixtures — which assert what was WRITTEN and
	// would otherwise need a cluster to do it.
	//
	// A POINTER AND NOT A BOOL BESIDE A STRUCT, because the struct's zero
	// value is meaningful and it is "wait": `&promoteFollowOptions{}` is a
	// real request for the default follow-through, and no bool
	// combination can express that as distinctly as the pointer does.
	Follow *promoteFollowOptions

	Gates []string
	From  promoteFromOptions
	// Targets is the deploy's scope (deployScopeTargets), recorded on the
	// promotion as release.Promotion.Targets. Nil for an unscoped deploy.
	Targets []string
	// Run is the run identity; resolved against the CI environment.
	Run runOptions

	// Ledger is the env's release ledger: where the promotion is recorded
	// AND — through its Hosted field — who applies it.
	//
	// REQUIRED, and resolved by the CALLER. `forge env deploy` resolves it
	// from the env's declaration (resolveReleaseLedger) before calling;
	// a test states one directly. Leaving it nil for runPromote to resolve
	// would make the field one no production path ever sets, so every read
	// here would observe only a test's value — the shape
	// internal/deadcodeguard calls a phantom field, and it is right to:
	// the seam would be exercised exclusively by the tests that use it.
	//
	// ONE SEAM AND NOT THREE. An earlier shape had Bindings, Releases and a
	// *bool Hosted as separate fields, which let a caller state a hosted
	// store and leave Hosted false — describing an env whose promotions
	// live on a control plane that then applies nothing. envLedger already
	// holds the three together and cannot express that: the hosted
	// constructor sets Hosted:true beside the stores, so where a promotion
	// is recorded and who converges it are decided once, from one
	// declaration.
	Ledger envLedger
	Git    promoteGitReader

	// Confirm is the O-13 approval gate, run between the plan and the write
	// (deploy_confirm.go). NIL MEANS NO GATE, and only the callers whose
	// subject is the ledger itself pass nil — the plan/CAS/gates/provenance
	// tests and `forge release`'s fixtures, exactly the callers that also
	// pass a nil Follow. `forge env deploy` always states it, so the verb
	// always shows the plan and asks before writing.
	Confirm *deployConfirm

	// Approval is the SERVER-BINDING half of the gate
	// (deploy_plan_gate.go): --approve's digest and the stop-class findings
	// --acknowledge-destructive named. Zero means neither was passed, which
	// is the interactive case — an operator approves the plan in front of
	// them.
	Approval deployApproval

	// DeployPlan is the §8.6 plan this deploy is judged against: PlanDeploy's
	// answer on a control-plane env, release.BuildPlan's locally on a file
	// ledger. Nil means no bundle-backed plan could be computed, which is a
	// real state rather than a failure — a never-built env, a control plane
	// that predates bundles (F-15), a registry that could not be reached.
	//
	// NIL DOES NOT MEAN "APPROVED". It means the plan's findings are
	// unavailable, so the digest and the destructive checks have nothing to
	// judge; the promote plan above is still shown and still confirmed. What
	// it must never do is let a stop-class finding through silently, which is
	// why --approve against a nil plan is refused rather than ignored.
	DeployPlan *release.Plan
	// Capacity is the pre-flight's verdict, shown in the plan and the summary.
	Capacity *promotePlanCapacity
}

// preparePromoteFollow validates follow-through and keeps one JSON owner for
// both promotion and apply output.
func preparePromoteFollow(follow *promoteFollowOptions, jsonOut bool) error {
	if follow == nil {
		return nil
	}
	if err := validatePromoteFollow(*follow); err != nil {
		return err
	}
	if jsonOut {
		follow.jsonOut = true
	}
	return nil
}

// runPromote computes the change set and — unless --plan was passed — applies
// it.
//
// PLAN THEN APPLY, ALWAYS, EVEN WITHOUT --plan. The real promote takes the
// identical path a dry run does and then writes; it does not have a second,
// leaner implementation. That is what makes the preview trustworthy: there is
// no code the write executes that the plan did not describe. It also means the
// success output is the change set rather than a digest dump, so the operator
// who skipped the preview still sees what moved.
//
// Resolving the digests at promote time (and snapshotting them into the
// binding) is unchanged and still deliberate: it makes "the bytes that passed
// staging ARE the bytes in prod" a checkable invariant — the digests are
// frozen the moment the env is promoted, independent of any later edit or move
// of the release file.
//
// EVERYTHING THAT CAN REFUSE THE PROMOTE IS CHECKED BEFORE THE WRITE. A flag
// this build does not support, a malformed gate, a bad run id: each refuses
// the whole command, rather than moving the pointer and then failing on the
// part the caller actually asked for.
func runPromote(ctx context.Context, version, env string, opts promoteOptions) error { //nolint:funlen // the promotion in order: resolve, validate every input, gate, move the pointer, record. Validating everything before the move is the contract (above).
	if ctx == nil {
		ctx = context.Background()
	}
	noteDeployPhase(ctx, "promote")
	noteDeployRelease(ctx, version)
	if err := preparePromoteFollow(opts.Follow, opts.JSON); err != nil {
		return err
	}
	gates, err := resolvePromoteGates(opts.Gates)
	if err != nil {
		return err
	}
	source, err := resolvePromoteFrom(ctx, version, env, opts.ProjectDir, opts.From)
	if err != nil {
		return err
	}
	version = source.Version
	if version == "" {
		return fmt.Errorf("name the release to deploy: forge env deploy %s <version>", env)
	}
	run, err := opts.Run.resolveRun()
	if err != nil {
		return err
	}

	projectDir := opts.ProjectDir
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}
	ledger := opts.Ledger
	if ledger.Bindings == nil || ledger.Releases == nil {
		return fmt.Errorf("internal: no release ledger resolved for env %q "+
			"(the caller must pass promoteOptions.Ledger — see resolveReleaseLedger)", env)
	}

	// The migration-order guard, ahead of BOTH promotion paths and of any
	// plan shown as approvable: a release env's schema has already passed
	// would be refused by env's own migrator at boot, after the write.
	if err := checkDeployMigrations(ctx, progressWriter(opts.JSON), projectDir, env, version, ledger, opts.AllowPassedMigrations); err != nil {
		return err
	}
	// The rollback floors, beside the migration guard and for the same
	// reason: ahead of both promotion paths and of any plan shown as
	// approvable. The writing backend judges again under its lock; this is
	// the refusal before anything is built or confirmed (floor.go).
	floorVerdict, floorTarget, err := checkDeployFloors(ctx, progressWriter(opts.JSON), projectDir, env, version, ledger, opts.Git, opts.RaiseFloorReason)
	if err != nil {
		return err
	}
	if handled, err := dispatchSnapshotPromotion(ctx, env, version, ledger, opts); handled || err != nil {
		return err
	}
	// A read, so it runs for --plan / --plan-only too: a release whose hosted
	// images cannot run on the platform's nodes is refused before any plan
	// is shown as approvable.
	if err := checkReleaseHostedPlatforms(ctx, progressWriter(opts.JSON), projectDir, env, version, ledger.Releases); err != nil {
		return err
	}

	plan, err := computePromotePlan(ctx, promotePlanOptions{
		Env:        env,
		Version:    version,
		ProjectDir: projectDir,
		Bindings:   ledger.Bindings,
		Releases:   ledger.Releases,
		Git:        opts.Git,
		// The scope rides the plan into the write (applyPromotePlan), and
		// is refused in the plan — before any preview is approvable —
		// where the env's control plane would converge more than it names.
		Scope:  opts.Targets,
		Ledger: ledger,
	})
	if err != nil {
		return err
	}
	plan.DryRun = opts.DryRun
	plan.SourceNote = source.Note
	// The §8.6 plan rides the document, so --json carries the digest a
	// two-stage pipeline passes to --approve and the codes it passes to
	// --acknowledge-destructive.
	plan.DeployPlan = opts.DeployPlan
	plan.Capacity = opts.Capacity
	plan.Floor = floorVerdict
	guard := guardFor(plan, opts.ExpectCurrent, opts.Supersede)
	guard.Target, guard.RaiseFloorReason = floorTarget, opts.RaiseFloorReason
	if guard.ExpectUnbound {
		plan.Expected = expectUnboundLiteral
	} else {
		plan.Expected = guard.ExpectedCurrentID
	}

	// THE PREFLIGHT IS PART OF THE PLAN. It runs before the approval gate
	// and the write — for --plan / --plan-only too, since it only reads — so
	// a release that cannot ship is refused with nothing recorded, and a
	// reviewer sees that refusal instead of approving a plan that cannot
	// ship. See preflightFollow.
	if opts.Follow != nil {
		if err := preflightFollow(ctx, env, version, projectDir, ledger, &plan, opts); err != nil {
			// A self-hosted delivery's grant refusal is a document too:
			// --json carries each missing permission as data, beside the
			// plan that could not ship.
			var refusal *fluxDeliveryGrantRefusal
			if opts.JSON && errors.As(err, &refusal) {
				plan.DeliveryGrants = refusal.Missing
				plan.stamp(err)
				if emitErr := emitJSONDocument(plan); emitErr != nil {
					return emitErr
				}
			}
			return err
		}
	}

	// THE CONFIRMATION GATE (O-13). The plan is printed and approved before
	// anything is written — see deploy_confirm.go for why the review had to
	// move in front of the write rather than beside it.
	//
	// It runs for BOTH deploy forms, because both write a promotion and a
	// promotion is the deploy. --plan was already a write-nothing preview, so
	// it skips the gate: there is no write to gate.
	//
	// Nil Confirm means "no gate", and the only callers that pass nil are the
	// ones whose subject IS the ledger — the plan/CAS/gates/provenance tests
	// and `forge release`'s fixtures, the same callers that pass a nil Follow.
	// Production always states it.
	// The SERVER-BINDING half of the gate (O-13), BEFORE the confirmation.
	//
	// The order matters: a plan the caller did not approve, or one carrying
	// an unacknowledged destructive change, must be refused without ever
	// prompting. Prompting first would ask a human to approve a deploy that
	// was going to be refused anyway, and — worse — would train them to
	// answer yes to a question whose answer does not decide anything.
	if !opts.DryRun {
		if err := gateDeployOnPlan(env, opts.DeployPlan, opts.Approval, opts.Confirm, opts.JSON); err != nil {
			return err
		}
	}

	if !opts.DryRun && opts.Confirm != nil {
		plan.renderForConfirmation(opts.JSON)
		outcome := confirmDeployPlan(env, plan, *opts.Confirm, opts.JSON)
		if outcome.Err != nil {
			return outcome.Err
		}
		if !outcome.Confirmed {
			// Declined, or --plan-only. Nothing was written, and that is a
			// SUCCESS: the caller asked what would happen and found out.
			plan.Confirmed = false
			// --plan-only's next_step is the command that approves THIS
			// plan, carrying the digest and the stop-class codes. It is the
			// string the gate already printed, not a second construction of
			// it: a pipeline reads next_step and a human reads the rendered
			// line, and those must be the same command or one of them is
			// wrong. The plan's default next_step ("forge env deploy <env>")
			// stays for every other caller.
			if outcome.NextStep != "" {
				plan.NextStep = outcome.NextStep
			}
			plan.stamp(nil)
			if opts.JSON {
				return emitJSONDocument(plan)
			}
			return nil
		}
		plan.Confirmed = true
	}

	// THE ONLY WRITE IN THIS COMMAND, and it is downstream of the plan. A
	// dry run simply skips it; everything rendered below is the same value
	// either way, which is why --plan cannot describe a different change
	// than the one that gets made.
	var writeErr error
	// A FLUX env's grants and bundle push, after the approval and before
	// the record: a failure leaves the ledger untouched
	// (flux_delivery_deploy.go).
	if !opts.DryRun && plan.flux != nil && opts.Follow != nil {
		if err := beforeRecordFluxDelivery(ctx, env, plan.flux, opts.Follow.notice); err != nil {
			return err
		}
	}
	if !opts.DryRun {
		writeErr = applyPromotePlan(ctx, ledger.Bindings, &plan, promoteWrite{
			By:    promoteActor(opts.Actor),
			Note:  opts.Note,
			Guard: guard,
			Gates: gates,
			Run:   run,
			// The resolved --from (promote_from.go).
			FromEnv:           source.FromEnv,
			FromPromotionID:   source.FromPromotionID,
			VersionFromSource: source.VersionFromSource,
			// The approved plan travels ON THE WRITE, so the server can
			// recompute it under the env row lock and refuse a mismatch
			// (F-19, exit 3). Without this the digest would be something
			// forge checked and the server took on trust, which is the
			// opposite of the guarantee — a client could approve a plan it
			// fabricated. Except where the plan is forge's own (a
			// ledger-only env): the server cannot recompute that one, so it
			// is checked here and recorded on the apply gate instead.
			PlanDigest:           serverPlanDigest(opts.DeployPlan, ledger),
			AcknowledgedFindings: opts.Approval.AcknowledgedFindings,
		})
		if writeErr == nil {
			plan.FloorsRaised = floorsRaisedBy(ctx, ledger, env, plan.Recorded)
		}
		if writeErr == nil && opts.Follow != nil {
			writeErr = followRecordedPromotion(ctx, env, projectDir, &plan, ledger, opts)
		}
	}
	// A refused write still renders: the plan is what the write WOULD have
	// done, and the refusal says what is there instead. An APPLIED promote
	// whose follow-through then failed renders too, and that case is the
	// one worth spelling out: a wait that degrades, times out or is
	// superseded arrives here in the same shape a failed write does,
	// but the two want opposite treatment. A failed write recorded
	// nothing, so there is no document worth reading beyond the error. A
	// failed WAIT recorded a promotion — `recorded.id` exists, and it is
	// precisely what the next pipeline step needs, because a red rollout
	// is exactly when the gate evidence must be attached to it. Dropping
	// the document there would leave CI's `gate record` with no promotion
	// id for the one release that needed the trail.
	//
	// plan.stamp(writeErr) below carries the follow-through's exit code
	// into ok/exit_code, so the document and the process status still
	// agree — the document reports applied:true with exit_code 5, which
	// is the truth: the pointer moved and the gate went red.
	if writeErr != nil && plan.Refusal == nil && !plan.Applied {
		return writeErr
	}

	// A QUEUED deploy is applied:true with exit_code 7, and `queued` says
	// what it waits on and where to act — the structured twin of the block
	// the error renders, so a pipeline reads action_url instead of text.
	if q := queuedOf(writeErr); q != nil {
		plan.Queued = q.document()
	}
	// A STILL-APPLYING deploy is applied:true with exit_code 8, and
	// `applying` names the bundle and the command that keeps watching.
	if p := applyingOf(writeErr); p != nil {
		recorded := ""
		if plan.Recorded != nil {
			recorded = plan.Recorded.ID
		}
		plan.Applying = applyingDocument(p, recorded)
	}
	if opts.Claim != nil && writeErr == nil {
		plan.Claim = opts.Claim()
	}
	plan.stamp(writeErr)
	if opts.JSON {
		if err := emitJSONDocument(plan); err != nil {
			return err
		}
		return writeErr
	}
	renderPromotePlanText(progressWriter(opts.JSON), plan)
	return writeErr
}

// followRecordedPromotion records every shipped stage even when follow-through
// fails, so the final plan can report what is already live.
func followRecordedPromotion(ctx context.Context, env, projectDir string, plan *promotePlan, ledger envLedger, opts promoteOptions) error {
	follow := *opts.Follow
	follow.projectDir = projectDir
	follow.planDigest = deployPlanDigest(opts.DeployPlan)
	shipped := &shipLog{}
	follow.clientDeploy.shipped = shipped
	err := asApplying(followPromote(ctx, env, *plan, ledger, follow))
	plan.recordFollow(shipped, err)
	return err
}

// preflightFollow runs every refusal the follow-through would otherwise meet
// AFTER the promotion is recorded but that depends on nothing the apply does.
// A refusal here writes nothing and ships nothing; the same refusal from the
// apply arrives with the binding moved and — on an env with several targets —
// with some of them already changed.
//
// Cheapest first. The convergence verdict is the declaration plus one read; the
// pin is one file; the cluster preflight renders the env and probes the live
// target; the hub check reads the hub's reconciler.
func preflightFollow(ctx context.Context, env, version, projectDir string, ledger envLedger, plan *promotePlan, opts promoteOptions) error {
	follow := *opts.Follow
	// What the env's control plane will converge of this deploy. See
	// deploy_promote_unpublished.go.
	converges, err := hostedConvergencePreflight(ctx, env, ledger, follow)
	plan.Converges = converges
	if err != nil {
		return err
	}
	// A PINNED env refuses the apply. --plan is exempt for the same reason
	// the apply's own gate exempts a dry run: a reviewer reading what WOULD
	// ship during a freeze is who the freeze is for.
	if !opts.DryRun && (ledger.appliesLocally() || ledger.flux()) {
		policyStore := deploystate.NewLocal(projectDir)
		if err := gateDeployOnPolicy(ctx, policyStore, env, policyStore.PolicyPath(env)); err != nil {
			return fmt.Errorf("%w\n\nNOTHING WAS RECORDED: env %q's binding did not move", err, env)
		}
	}
	// A FLUX env's preflight: the bundle it will point at, the input that
	// pointer carries, and the grants the release needs. A refusal writes
	// nothing; a grant this deploy makes becomes a finding on the plan.
	if ledger.flux() {
		rel, err := preflightFluxDelivery(ctx, env, version, projectDir, ledger, opts.DeployPlan, follow.notice)
		if err != nil {
			return err
		}
		plan.flux = rel
		plan.DeliveryGrants = fluxDeliveryGrantRows(rel.grants.Missing)
	}
	nonBinding := opts.DryRun || follow.clientDeploy.dryRun || (opts.Confirm != nil && opts.Confirm.PlanOnly)
	findings, err := preflightBeforeRecord(ctx, env, version, ledger, follow, nonBinding)
	if err != nil {
		return err
	}
	plan.FrontendPreflight = findings
	// The converger's half of the same question, for an env the hub
	// applies: can anything apply this at all? See deploy_hub_ready.go.
	if follow.skipHubCheck {
		return nil
	}
	return refuseUnreadyHub(ctx, env, ledger, plan.Current.Bound, follow)
}

// resolveReleaseLedger is how `forge env deploy` answers both halves of "where
// does this release live" before it starts: WHERE the promotion is recorded,
// and — through envLedger.Hosted — WHO applies it.
//
// It is the production writer of promoteOptions.Ledger, and it exists as its
// own function so that fact is visible at the command rather than buried as a
// nil-check inside runPromote. Resolving it there would have made the seam a
// field only tests ever set.
func resolveReleaseLedger(ctx context.Context, projectDir, env string) (envLedger, error) {
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}
	// The one render the ledger selection reads anyway, rendered here so an
	// `applier = "flux"` env is known as one before anything is built or
	// recorded: its follow-through points its Flux instead of applying.
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", env, "main.k")); err == nil {
		entities, err := RenderKCL(ctx, projectDir, env)
		if err != nil {
			return envLedger{}, fmt.Errorf("choose the release ledger for env %q: render deploy/kcl/%s: %w", env, env, err)
		}
		l, err := ledgerForRendered(ctx, projectDir, env, entities)
		if err != nil {
			return envLedger{}, err
		}
		if entities.Applier == applierFlux && entities.FluxDelivery != nil {
			if l.Hosted {
				// The schema refuses applier = "flux" beside a control
				// plane; a ledger override cannot make one hosted either.
				return envLedger{}, fmt.Errorf("env %q declares applier = \"flux\" but resolves to a hosted ledger; a self-hosted delivery records in forge's file ledger", env)
			}
			l.FluxEntities = entities
		}
		return l, nil
	}
	return ledgerFor(ctx, projectDir, env)
}

// promoteActor is who the ledger entry names. An explicit --actor is an
// automation; otherwise the local user, best-effort. The hosted ledger
// ignores this for the human half and records the authenticated caller.
func promoteActor(actor string) release.Actor {
	if actor != "" {
		return release.Actor{Actor: actor}
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return release.Actor{User: u.Username}
	}
	return release.Actor{}
}
