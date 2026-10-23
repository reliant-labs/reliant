package cli

// WHERE THE §8.6 PLAN COMES FROM, and why the answer differs by backend.
//
//   - A CONTROL-PLANE env's plan comes from PlanDeploy, computed server-side
//     against the RECORDED bundle. That is not a performance choice. The
//     server recomputes the plan again at approval time, under the env row
//     lock, and refuses the write unless the digest matches — so the plan has
//     to be the server's, or the digest forge showed could never be the one
//     the server derives. Forge verifies the returned digest client-side
//     (hosted_plan.go), which catches a version skew now rather than as a
//     misleading plan_stale at approval time. That holds only where the
//     control plane can SEE what it plans against: a placed env (its own
//     deployments) or a hub-converged one (its Flux).
//   - A LEDGER-ONLY env's plan is built HERE (deploy_plan_applied.go).
//     Its control plane records promotions and converges none of it, so it
//     has no Live at all; forge is the only party that applies it, and so the
//     only one that can record what was applied and read the cluster's
//     Secrets.
//   - A FILE-LEDGER env's plan is built HERE, by release.BuildPlan, because
//     there is no server. Both sides call the same function, so the two
//     backends cannot disagree about what a finding means — only about what
//     Live is, which is the thing they genuinely see differently.
//
// THE LIVE SIDE IS THE APPLIED BUNDLE, NEVER THE DECLARED SHAPE (briefing
// §7). `forge env build` refreshes declared_shape from the very render being
// deployed, so diffing against it would hide every change — the plan would
// report "no differences" precisely when there were some. For an env forge
// applies, the applied bundle is the newest one with a SUCCEEDED apply; when
// there is none, Live is nil, which BuildPlan reports as an `unknown` warn
// rather than as an empty diff (F-20: empty and unknown must never render
// alike).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/reliant-labs/forge/pkg/release"
)

// planForDeploy is the plan a deploy of (env, version) is judged against.
//
// It resolves the bundle the deploy will apply — the newest one recorded for
// this env and this release — and plans against it.
//
// NEWEST FOR (env, release), because that is what a deploy of that release
// applies. A release is env-agnostic and a bundle is not, so one release has
// one bundle per env; re-running `forge env build` for the same release after
// a KCL edit records a second, and the newest is the one the next deploy ships.
//
// EVERY FAILURE HERE IS NON-FATAL, and that is the deliberate shape. The plan
// INFORMS the gate; it is not the gate. A deploy whose plan could not be
// computed still faces the confirmation prompt, still cannot proceed without
// approval, and — on a control plane — is still judged server-side at the
// write. Refusing the deploy because the plan was unavailable would make every
// transient read failure an outage, and the one thing a missing plan must not
// do is let a stop finding through silently, which gateDeployOnPlan handles by
// refusing --approve rather than by admitting the write.
func planForDeploy(ctx context.Context, projectDir, env, version string, targets []string, ledger envLedger, errOut io.Writer) *release.Plan {
	digest, err := bundleDigestForRelease(ctx, projectDir, env, version, ledger)
	if err != nil || digest == "" {
		if err != nil {
			fmt.Fprintf(errOut, "[plan] Note: env %s's bundle for %s could not be resolved (%v); "+
				"no deploy plan was computed.\n", env, emptyAs(version, "this deploy"), err)
		}
		return nil
	}
	plan, err := resolveDeployPlan(ctx, projectDir, env, ledger, digest, errOut)
	if err != nil {
		fmt.Fprintf(errOut, "[plan] Note: env %s's deploy plan could not be computed (%v); "+
			"the deploy continues behind the confirmation gate.\n", env, err)
		return nil
	}
	return withChartFindings(ctx, projectDir, env, targets, plan, errOut)
}

// bundleDigestForRelease is the digest of the bundle a deploy of (env,
// version) applies, or "" when none is recorded.
//
// IT READS THE MACHINE LEDGER FOR BOTH BACKENDS, which is a real limitation
// and not an oversight. A bundle is content-addressed, so its digest is a
// property of the bytes rather than of the store that indexes them — and the
// build that pushed a hosted env's bundle recorded the same digest locally on
// its way out. So the lookup works for a deploy run from the machine that
// built it, which is every deploy today.
//
// What it does NOT cover is a deploy from a DIFFERENT machine than the build:
// CI builds, an operator deploys. There the local ledger has no row, this
// returns "", and the deploy proceeds with no plan — gated by the confirmation
// and, on a control plane, still judged server-side at the write.
//
// The fix is a server-side lookup by (env, release), which needs an RPC that
// does not exist yet: GetBundle takes an id or a digest, and ListBundles is
// not in the doc's §6.3 list at all. Raised with the orchestrator rather than
// papered over, because the honest degradation (no plan, gate still stands) is
// safe and inventing a client-side substitute would not be.
func bundleDigestForRelease(_ context.Context, projectDir, env, version string, ledger envLedger) (string, error) {
	// A hosted env's bundle is recorded on the control plane, not in the
	// machine ledger, so the digest this process just recorded is the one
	// to plan against.
	if ledger.Hosted {
		if digest := recordedBundleDigest(env, version); digest != "" {
			return digest, nil
		}
	}
	store, err := recordStoreFor(projectDir)
	if err != nil {
		return "", err
	}
	bundles, err := store.store.Bundles(env)
	if err != nil {
		return "", err
	}
	// Oldest first, so walk backwards for the newest matching bundle.
	for i := len(bundles) - 1; i >= 0; i-- {
		if bundles[i].Release == version {
			return bundles[i].Digest, nil
		}
	}
	return "", nil
}

// resolveDeployPlan computes the §8.6 plan for deploying a bundle to an env.
//
// A nil plan with a nil error is a REAL STATE, not a failure: there is no
// recorded bundle to plan against (a never-built env), or the control plane
// predates bundles (F-15). The caller shows the promote plan and confirms as
// usual; what it must not do is treat nil as approval, which gateDeployOnPlan
// enforces.
func resolveDeployPlan(ctx context.Context, projectDir, env string, ledger envLedger, bundleDigest string, errOut io.Writer) (*release.Plan, error) {
	switch {
	case ledger.Offline != nil:
		// The plan is the control plane's (it holds the recorded bundles);
		// under --offline-ledger there is none to ask. The deploy proceeds
		// behind the confirmation gate, as for any env with no plan.
		fmt.Fprintf(errOut, "[plan] --offline-ledger: env %s's deploy plan needs the control plane, so none was computed.\n", env)
		return nil, nil
	case ledger.forgePlans():
		return ledgerOnlyDeployPlan(ctx, projectDir, env, ledger, bundleDigest, errOut)
	case ledger.Hosted:
		return hostedDeployPlan(ctx, projectDir, env, bundleDigest, errOut)
	default:
		return fileLedgerDeployPlan(ctx, projectDir, env, bundleDigest)
	}
}

// hostedDeployPlan asks the control plane.
//
// F-15 LIVES HERE. A control plane that predates bundles answers Unimplemented
// on PlanDeploy, which is not a refusal of this deploy — it is a server that
// cannot compute a plan at all. So forge says what the doc says to say, falls
// back to the pre-bundle path, and the deploy proceeds under the confirmation
// gate alone. Once protected envs exist (#516) a protected env refuses instead
// (exit 2); there are no protected envs before then, so there is nothing to
// refuse on yet.
func hostedDeployPlan(ctx context.Context, projectDir, env, bundleDigest string, errOut io.Writer) (*release.Plan, error) {
	store, err := hostedRecordStoreForDeploy(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	envID, err := store.envID(ctx, env)
	if err != nil {
		return nil, err
	}
	// The plan is computed against a RECORDED bundle, so the bundle has to
	// be looked up by the digest forge built — the id is the server's to
	// assign.
	recorded, err := store.Bundles().GetBundleByDigest(ctx, env, envID, bundleDigest)
	if err != nil {
		if plan, handled := planUnavailable(err, env, errOut); handled {
			return plan, nil
		}
		return nil, err
	}
	if recorded == nil {
		// Recorded nowhere. Not an error: a deploy of an env whose bundle
		// was never recorded is exactly the state F-14 admits with a
		// finding, and the confirmation gate still stands in front of it.
		fmt.Fprintf(errOut,
			"[plan] env %s has no recorded bundle for %s, so no deploy plan could be computed.\n"+
				"[plan]   The deploy continues behind the confirmation gate; `forge env build %s` records one.\n",
			env, shortDigest(bundleDigest), env)
		return nil, nil
	}
	plan, err := store.Plans().PlanDeploy(ctx, envID, recorded.ID, recorded.Release)
	if err != nil {
		if fallback, handled := planUnavailable(err, env, errOut); handled {
			return fallback, nil
		}
		return nil, err
	}
	return &plan, nil
}

// planUnavailable is the F-15 branch: the control plane cannot hold or compute
// a bundle-backed plan.
//
// It is recognised by the typed error F4 defined, never by message text — an
// old server's prose is not a contract, and one that improved its wording
// would silently stop matching.
func planUnavailable(err error, env string, errOut io.Writer) (*release.Plan, bool) {
	if !errors.Is(err, errControlPlanePredatesBundles) {
		return nil, false
	}
	fmt.Fprintf(errOut,
		"[plan] %s\n"+
			"[plan]   Env %s is deployed on the pre-bundle path: no server-side plan, and the deploy is gated by\n"+
			"[plan]   the confirmation alone. Upgrading the control plane restores the plan.\n",
		errControlPlanePredatesBundles.Error(), env)
	return nil, true
}

// fileLedgerDeployPlan builds the plan locally, from the machine ledger's own
// records.
//
// It is the same release.BuildPlan the server runs, over the same inputs the
// server would use: the candidate bundle's shape against the APPLIED bundle's.
// What a file ledger cannot supply is an observer — nothing watches that
// cluster — so Drift is nil, which BuildPlan reports as an `unknown` warn
// rather than as "no drift". Claiming a clean cluster forge never looked at
// would be the one answer worse than admitting it did not look.
func fileLedgerDeployPlan(ctx context.Context, projectDir, env, bundleDigest string) (*release.Plan, error) {
	store, err := recordStoreFor(projectDir)
	if err != nil {
		return nil, err
	}
	bundles, err := store.store.Bundles(env)
	if err != nil {
		return nil, err
	}
	candidate, ok := bundleWithDigest(bundles, bundleDigest)
	if !ok {
		return nil, nil
	}

	live, basis, err := appliedBundleShape(ctx, store, env, bundles)
	if err != nil {
		return nil, err
	}
	basis.AppliedConfigDigest = ""
	if live != nil {
		basis.AppliedConfigDigest = appliedConfigDigest(bundles, basis.AppliedBundleID)
	}
	if current, bound, cerr := store.store.CurrentPromotion(env); cerr == nil && bound {
		basis.CurrentPromotionID = current.ID
	}

	plan, err := release.BuildPlan(release.PlanInput{
		EnvironmentID:         env,
		BundleID:              candidate.ID,
		ReleaseVersion:        candidate.Release,
		Candidate:             candidate.Shape,
		CandidateConfigDigest: candidate.ConfigDigest,
		Live:                  live,
		Basis:                 basis,
		// Drift: nil — a file-ledger env has no observer. BuildPlan
		// reports that as unknown/warn (briefing §7, F-20).
		// SecretPresence: the cluster's own answer for `external` secrets,
		// read keys-only (deploy_plan_secrets.go). A name it could not
		// read stays out of the map, which #398 makes UNVERIFIABLE rather
		// than missing.
		SecretPresence: deployPlanSecretPresence(ctx, projectDir, env, candidate.Shape),
	})
	if err != nil {
		return nil, err
	}
	if live != nil {
		for _, b := range bundles {
			if b.ID == basis.AppliedBundleID {
				annotateChangedFields(ctx, projectDir, env, &plan, b.Reference, candidate.Reference)
				break
			}
		}
	}
	// forge applies it, and nothing prunes.
	noteDirectApplyRemovals(&plan)
	return &plan, nil
}

// appliedBundleShape is the Live side: the newest bundle with a SUCCEEDED
// apply.
//
// NEWEST SUCCEEDED, not newest recorded, and the difference is the whole
// point. A bundle that was recorded and whose apply failed, timed out or was
// abandoned does not describe what is running — diffing against it would
// report the failed deploy's changes as already applied, and hide them from
// the plan that is about to apply them for real.
//
// No succeeded apply at all means nil, which BuildPlan turns into an `unknown`
// warn. That is the honest answer for a first deploy and for an env whose
// history forge cannot see.
func appliedBundleShape(_ context.Context, store machineRecordStore, env string, bundles []release.BundleRecord) (*release.Shape, release.PlanBasis, error) {
	applies, err := store.store.Applies(env)
	if err != nil {
		return nil, release.PlanBasis{}, err
	}
	// Applies come oldest first, so walk backwards for the newest.
	for i := len(applies) - 1; i >= 0; i-- {
		r := applies[i]
		if r.Outcome == nil || r.Outcome.Status != release.ApplySucceeded {
			continue
		}
		for j := range bundles {
			if bundles[j].ID != r.Apply.BundleID {
				continue
			}
			shape := bundles[j].Shape
			return &shape, release.PlanBasis{AppliedBundleID: bundles[j].ID}, nil
		}
	}
	return nil, release.PlanBasis{}, nil
}

// appliedConfigDigest is the applied bundle's config digest, which is what
// ConfigIdentical compares against — the short-circuit that lets a no-op
// deploy say "config identical" instead of manufacturing a diff to approve.
func appliedConfigDigest(bundles []release.BundleRecord, bundleID string) string {
	for _, b := range bundles {
		if b.ID == bundleID {
			return b.ConfigDigest
		}
	}
	return ""
}

// bundleWithDigest finds the candidate among an env's recorded bundles.
func bundleWithDigest(bundles []release.BundleRecord, digest string) (release.BundleRecord, bool) {
	for _, b := range bundles {
		if b.Digest == digest {
			return b, true
		}
	}
	return release.BundleRecord{}, false
}

// hostedRecordStoreForDeploy resolves the hosted records store for an env a
// deploy is about to plan against.
//
// A var for the reason bundleLedgerFor is: a test of the PLAN's rules must be
// able to state which backend answered without standing up a control plane and
// a credential.
var hostedRecordStoreForDeploy = func(ctx context.Context, projectDir, env string) (hostedRecordStore, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return hostedRecordStore{}, fmt.Errorf("render deploy/kcl/%s to reach its control plane: %w", env, err)
	}
	client, _, err := envDeclarationClient(env, entities)
	if err != nil {
		return hostedRecordStore{}, err
	}
	return hostedRecordStoreFor(client, hostedEnvRefFor(env, entities).Project), nil
}

var (
	recordedBundleMu      sync.Mutex
	recordedBundleDigests = map[string]string{}
)

// noteRecordedBundles remembers the digests of bundles this process recorded,
// keyed by (env, release), so the plan step can find a hosted env's bundle
// without a control-plane lookup by release (no such RPC exists).
func noteRecordedBundles(version string, outcomes []bundleWriteOutcome) {
	if version == "" {
		return
	}
	recordedBundleMu.Lock()
	defer recordedBundleMu.Unlock()
	for _, o := range outcomes {
		if o.Skipped || !o.Recorded || o.Digest == "" {
			continue
		}
		recordedBundleDigests[o.Env+"\x00"+version] = o.Digest
	}
}

func recordedBundleDigest(env, version string) string {
	recordedBundleMu.Lock()
	defer recordedBundleMu.Unlock()
	return recordedBundleDigests[env+"\x00"+version]
}
