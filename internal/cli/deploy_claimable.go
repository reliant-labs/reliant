package cli

// `forge env deploy <env> --claimable`: a deploy with no account.
//
// An agent with no Reliant account runs the flag and gets two things back: a
// live URL, and a CLAIM LINK it hands to its human. Unless a human opens the
// link the deploy expires. The design is control-plane's
// docs/design/claimable-deploys.md (§6 is this file's spec).
//
// THE PATH RUNS ONLY WHEN ASKED FOR. There is no auto-detection: a missing
// credential stays a missing credential (its message now names the flag), and
// a signed-in user who passes --claimable is refused, so nobody creates an
// orphan preview by accident.
//
// HOW IT FITS THE ORDINARY DEPLOY. This is a separate branch in front of
// runDeployEverything, not a second deploy. It (1) checks no real credential
// resolves, (2) checks the render fits what an unclaimed deploy may be, (3)
// obtains the anonymous token (the stored one, or StartClaimableDeploy behind
// a proof of work), (4) installs the token for this process only, so the
// unchanged hosted publish runs as the anonymous user, and (5) prints the claim.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/deploy"
	v1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The refusal reasons forge itself raises on this path, beside the control
// plane's (cloud.ReasonClaimable*).
const (
	reasonClaimableSignedIn   = "claimable_signed_in"
	reasonClaimableIneligible = "claimable_ineligible"
)

// claimableMigrationNote is what an anonymous deploy does instead of the
// release cut's unmerged-migration refusal (unmergedMigrationGuard): it
// applies the migrations of the working tree it runs from, committed or not,
// and says so in one line. The refusal exists so a SHARED env's schema never
// gets ahead of main; an unclaimed env is a throwaway preview no team's main
// races, and "no account, repository or commit needed" is its promise. After
// the claim, a signed-in deploy is held to the refusal again, and the passed-
// migration check still guards every later release. Judged from the checkout
// before an anonymous deploy solves a proof of work, mints an account or
// builds anything, and by --plan-only. It returns an error only when the
// migrations directory cannot be read.
func claimableMigrationNote(out io.Writer, projectDir string, e *KCLEntities) error {
	if !envKeepsHostedLedger(e) || foreignToolGuardNote(projectDir) != "" {
		return nil
	}
	u, err := findUnmergedMigrations(projectDir)
	if err != nil {
		return err
	}
	switch {
	case len(u.files) > 0:
		fmt.Fprintf(out, "[deploy] note: an anonymous deploy applies the migrations in this working tree as they are, so %d not on %s (%s) need no commit (a signed-in deploy refuses them until they are on it)\n",
			len(u.files), u.ref, strings.Join(u.files, ", "))
	case u.noDefaultBranch:
		fmt.Fprintln(out, "[deploy] note: an anonymous deploy applies the migrations in this working tree as they are; no commit is needed")
	}
	return nil
}

// The claimable plan's caps are the control plane's, read from its public
// GetClaimableAvailability answer (claimable_limits.go): the one value the
// hosted plan's resource rule and these checks both judge by.

// claimablePreview is set while a credential-less preview judges an env under
// the claimable plan (`forge env shape --claimable`, `forge env deploy
// --claimable --plan-only`). The real deploy needs no flag: it installs the
// anonymous credential (cloud.ClaimableActive).
var claimablePreview atomic.Bool

// useClaimablePreview judges every hosted group built until restore runs
// under the claimable plan.
func useClaimablePreview() (restore func()) {
	prev := claimablePreview.Swap(true)
	return func() { claimablePreview.Store(prev) }
}

// declaresClaimable reports whether the env declares itself an anonymous
// deploy (`control_plane = forge.ControlPlane {claimable = True}`).
func declaresClaimable(e *KCLEntities) bool {
	return e != nil && e.ControlPlane != nil && e.ControlPlane.Claimable
}

// hostedPlanOf is the plan an env's hosted group is judged under: claimable
// when this process deploys anonymously, previews an anonymous deploy, or the
// env declares itself one; standard otherwise.
func hostedPlanOf(e *KCLEntities) deploy.HostedPlan {
	if cloud.ClaimableActive() || claimablePreview.Load() || declaresClaimable(e) {
		return deploy.HostedPlanClaimable
	}
	return deploy.HostedPlanStandard
}

// refuseClaimableEnvSignedIn refuses a signed-in deploy of an env declared
// anonymous: its shapes are sized for the claimable plan, which an
// organization's own deploy is not admitted under.
func refuseClaimableEnvSignedIn(env string, e *KCLEntities, cred cloud.Credential) error {
	if !declaresClaimable(e) || cred.Source == cloud.SourceClaimable {
		return nil
	}
	return &claimableRefusal{
		exit: exitWrong, code: cloud.CodeFailedPrecondition, reason: reasonClaimableSignedIn,
		msg: fmt.Sprintf("env %q is declared for an anonymous deploy (control_plane = forge.ControlPlane {claimable = True}), "+
			"but you are signed in (%s: %s).\n"+
			"  to deploy it to your organization: in deploy/kcl/%s/main.k remove `claimable = True` and the anonymous-plan "+
			"`resources` of its hosted workloads (the default 250m / 1 GiB is on the hosted shape band), then %s env deploy %s\n"+
			"  to deploy it anonymously instead: sign out (forge logout), then %s env deploy %s --claimable",
			env, cred.Source, cred.From, env, Name(), env, Name(), env),
		next: []string{fmt.Sprintf("%s env deploy %s", Name(), env), fmt.Sprintf("forge logout && %s env deploy %s --claimable", Name(), env)},
	}
}

// claimableAPI builds the unauthenticated ClaimableService client. A var so the
// CLI tests state the control plane's answers.
var claimableAPI = func(ep cloud.Endpoint) cloud.ClaimableAPI { return cloud.NewClaimableClient(ep) }

// claimableNow is the clock the throttle's "retry at" and the claim's "in 72h"
// are computed from. A var for the tests.
var claimableNow = time.Now

// claimableSession is one anonymous deploy as this invocation sees it: where it
// lives, its token, and its claim facts. It is what the output renders.
type claimableSession struct {
	ep      cloud.Endpoint
	project string
	env     string
	cred    credentials.Credential
	// reused is true when the stored deploy of an earlier run was used.
	reused bool
}

// deployClaimJSON is the `claim` object of the --json document (§6): present on
// a claimable deploy, absent on every ordinary one.
type deployClaimJSON struct {
	// State is live | suspended | claimed | expired: the claim's lifecycle.
	// "live" means the unclaimed window is open, not that anything serves:
	// read Published for that.
	State string `json:"state"`
	// Published is false while no deploy of this claimable deploy has
	// published (as far as this machine knows): the claim link and expiry
	// exist from the first attempt, so an env whose every attempt failed has
	// both and nothing behind them. Always present.
	Published bool `json:"published"`
	// LastAttempt is how the newest deploy from this machine ended, when it
	// is known.
	LastAttempt *claimAttemptJSON `json:"last_attempt,omitempty"`
	// ClaimRequired is set when the newest deploy was refused until the
	// deploy is claimed, and says why (the control plane's
	// x-forge-claim-required value, e.g. "content"). The claim link is then
	// the way forward, so agent_instructions says to give it out even when
	// nothing is published.
	ClaimRequired string `json:"claim_required,omitempty"`
	// URL is the deploy's live address; empty when it has none yet.
	URL string `json:"url,omitempty"`
	// APIURL is where the deploy's API answers on the site's own host
	// (<site origin>/_api): set when the env's hosted site points API_URL
	// at its hosted backend, which the platform then serves there. Empty
	// otherwise, and while URL is.
	APIURL string `json:"api_url,omitempty"`
	// ClaimURL is the link a human opens to own the deploy. Sensitive: whoever
	// opens it first owns the deploy.
	ClaimURL string `json:"claim_url"`
	// ExpiresAt is when the deploy goes offline; PurgeAfter when it is deleted.
	ExpiresAt  time.Time `json:"expires_at"`
	PurgeAfter time.Time `json:"purge_after"`
	// AgentInstructions is what the agent must do with the above.
	AgentInstructions string `json:"agent_instructions"`
	// NoOutboundInternet is true when the deploy runs a service: an unclaimed
	// backend can serve requests and use its own database but cannot open a
	// connection to, or resolve a name on, the internet until it is claimed.
	// Absent for a static site, which makes no outbound calls.
	NoOutboundInternet bool `json:"no_outbound_internet,omitempty"`
	// ConsoleURL is the project's page in the Reliant console: where the
	// person who claims the deploy finds its releases, logs and dashboards.
	// Empty when forge does not know the control plane's web app.
	ConsoleURL string `json:"console_url,omitempty"`
}

// afterClaimNote is what the person who claims a deploy gets, for the agent to
// pass on. Every clause is the control plane's behaviour on claim
// (control-plane internal/claimable: the deploy's org becomes theirs, the
// agent's claimable token is revoked, the org follows the normal plan rules:
// free static hosting covers one organization per person, with no limit on
// its sites, and anything else needs hosting billing).
func afterClaimNote(env string) string {
	return fmt.Sprintf("Once claimed it is theirs, in their own Reliant account (free to sign up): free static hosting "+
		"covers one organization per person, with no limit on its sites, and a backend or database needs hosting billing on their account. "+
		"This machine's claimable credential stops working at the claim; to redeploy as them: %s login, then %s env deploy %s",
		Name(), Name(), env)
}

// claimAttemptJSON is the `claim.last_attempt` object.
type claimAttemptJSON struct {
	// At is when the deploy ended.
	At time.Time `json:"at"`
	// Outcome is published | failed.
	Outcome string `json:"outcome"`
	// Error is the first line of why it failed; empty when it published.
	Error string `json:"error,omitempty"`
}

// noOutboundNote is the one explanation of why an unclaimed backend's calls to
// the internet fail. It is the same sentence in every surface that carries it
// (the claim block, the --json instructions, the preview, the skills).
const noOutboundNote = "An unclaimed backend has no outbound internet: it serves requests and uses its own " +
	"database, but any call out of it (a payment, an LLM, a webhook, a package or DNS lookup of an outside name) " +
	"fails with a connection error or a name that does not resolve; only the cluster's own names, such as its " +
	"database's, resolve. That is the platform, not a bug in the app. " +
	"It ends when the deploy is claimed: the platform restarts the backend on the normal hosted network."

// claimableRefusal is a refusal this path raises itself (or a throttle it
// translated): an error that carries its exit code and the machine-readable
// facts the --json envelope reports.
type claimableRefusal struct {
	exit int
	// code is the Connect code name; reason the stable branch-on reason.
	code, reason string
	msg          string
	// throttle is set for claimable_throttled.
	throttle *claimableThrottleFacts
	// cause is set for claimable_unavailable: the first reason why.
	cause *cloud.ClaimableCause
	// emitted is true when the deploy's own --json document already carries
	// this outcome, so it must not be written a second time.
	emitted bool
	// platformErr is the platform error this refusal renders, when there is
	// one: Unwrap returns it, so its reason and headers stay readable
	// (cloud.ClaimRequiredOf).
	platformErr error
	// next are the exact commands that move past this refusal, best first
	// (the --json `next_actions`). msg ends with the first of them, so an
	// agent reading only the text has the command too.
	next []string
}

type claimableThrottleFacts struct {
	scope      string
	retryAfter time.Duration
	retryAt    time.Time
}

func (e *claimableRefusal) Error() string { return e.msg }
func (e *claimableRefusal) ExitCode() int { return e.exit }
func (e *claimableRefusal) Unwrap() error { return e.platformErr }

// refuseClaimableFlags rejects the flag combinations that make no sense for a
// deploy that creates an anonymous account: anything that previews, scopes, or
// names a release.
func refuseClaimableFlags(envName string, f deployCmdFlags) error {
	bad := func(flag, why string) error {
		next := fmt.Sprintf("%s env deploy %s --claimable", Name(), envName)
		if flag == "--plan" || flag == "--dry-run" {
			next += " --plan-only"
		}
		return &claimableRefusal{
			exit: exitWrong, code: cloud.CodeInvalidArgument,
			msg:  fmt.Sprintf("--claimable cannot be combined with %s: %s\n  run instead: %s", flag, why, next),
			next: []string{next},
		}
	}
	switch {
	case f.offlineLedger:
		return bad("--offline-ledger", "an anonymous deploy always talks to the control plane")
	case f.explain:
		return bad("--explain", "it describes a cluster apply, and an anonymous deploy has none")
	case f.dryRun:
		return bad("--dry-run", "the deploy creates the anonymous account; there is nothing to preview without it")
	case f.promote.plan && !f.promote.planOnly:
		return bad("--plan", "an anonymous deploy has no account to approve a plan for; preview it with --plan-only, which checks it locally and creates nothing")
	case f.promote.requestedRelease():
		return bad("a release version or --from", "--claimable ships this checkout; promote a release with your own account")
	case len(f.targets) > 0 || f.frontendsOnly || f.skipFrontend:
		return bad("--target/--frontends-only/--skip-frontend", "an anonymous deploy publishes the whole env")
	}
	return nil
}

// dispatchClaimableDeploy is `forge env deploy <env> --claimable`.
func dispatchClaimableDeploy(ctx context.Context, envName string, f deployCmdFlags) error {
	report := newDeployReport(envName, f.jsonOut)
	out := progressWriter(f.jsonOut)
	// What a refusal reports (claimable_telemetry.go): the deploy's shape once
	// the env rendered, the step that refused when the error does not say,
	// and whether the refusal is the control plane's (its availability
	// verdict and the start), which forge does not count as a local one.
	var (
		shape       cloud.DeployShape
		stage       string
		serverHeard bool
	)
	err := func() error {
		stage = refusedInvalidFlags
		if err := refuseClaimableFlags(envName, f); err != nil {
			return err
		}
		stage = ""
		adopted, err := adoptBeforeClaimableDeploy(ctx, out)
		if err != nil {
			return err
		}
		ctx = withAdoptedPaths(ctx, adopted)
		projectDir := projectDirForKCL()
		entities, err := RenderKCL(ctx, projectDir, envName)
		if err != nil {
			return err
		}
		// From here every control-plane call names the deploy's shape.
		shape = claimableShapeOf(entities)
		ctx = cloud.WithDeployShape(ctx, shape)
		if entities == nil || !entities.HasHosted() || entities.ControlPlane == nil {
			return fmt.Errorf("env %q publishes nothing to a hosted control plane, so there is nothing to deploy with --claimable.\n"+
				"fix: bind the env's workloads to forge.OnHosted (deploy/hosting), then: %s env deploy %s --claimable", envName, Name(), envName)
		}
		if envAppliesLocally(entities) {
			return fmt.Errorf("env %q applies part of itself from this machine (a cluster, compose, host or forge.OnExternal part), "+
				"which an anonymous deploy cannot do. --claimable publishes only hosted workloads.\n"+
				"  deploy it to your own account instead: forge login, then %s env deploy %s", envName, Name(), envName)
		}
		ep, err := cloud.ResolveEndpoint(envName, declarationFromEntities(entities))
		if err != nil {
			return err
		}
		// One availability answer serves the caps every check below judges
		// by, the preview and the preflight. Asking it refuses nothing: the
		// checks between it and requireClaimableAvailable (signed in,
		// admission, migrations) are forge's own refusals and are counted.
		answer := askClaimableAvailability(ctx, ep)
		limits := claimableLimitsFrom(ep, answer.av, answer.ok, answer.err)
		defer useClaimableLimits(limits)()
		if f.promote.planOnly {
			return previewClaimable(envName, entities, ep, answer, limits, claimableMigrations{projectDir}, f.jsonOut)
		}
		fmt.Fprintf(out, "  %s\n", limits.note())
		if err := refuseClaimableWhenSignedIn(ep, envName); err != nil {
			return err
		}
		if reasons := claimableAdmission(envName, entities, limits.limits); len(reasons) > 0 {
			return claimableIneligibleError(envName, limits.limits, reasons)
		}
		stage = refusedUnmergedMigrations
		if err := claimableMigrationNote(out, projectDir, entities); err != nil {
			return err
		}
		stage = ""
		project := hostedProjectName()
		if project == "" {
			return fmt.Errorf("--claimable needs the project's name: set `name` in forge.yaml, then: %s env deploy %s --claimable", Name(), envName)
		}
		// Before any proof of work, any account and any build: can the
		// platform take this env anonymously at all right now?
		_, storedErr := cloud.LoadClaimable(ep, project, envName, claimableNow())
		// From the platform's availability verdict through the start, a
		// refusal is the control plane's own decision, not a local one.
		serverHeard = true
		if err := requireClaimableAvailable(answer, ep, envName, entities, storedErr != nil); err != nil {
			return err
		}
		session, err := obtainClaimable(ctx, out, ep, project, envName)
		if err != nil {
			return err
		}
		serverHeard = false
		restore := cloud.UseClaimableCredential(ep, session.cred.Token, session.cred.ExpiresAt)
		defer restore()
		compute := needsCompute(entities)
		// The registry login of an anonymous deploy lands in a throwaway
		// docker config, so it does not write to the user's ~/.docker. No
		// throwaway, no deploy: the login never falls back to the real one.
		restoreDocker, err := isolateDockerConfig(compute, platformRegistryHost(entities))
		if err != nil {
			return err
		}
		defer restoreDocker()

		// Everything the ordinary no-version deploy does, as the anonymous user.
		// --yes is implied: there is no human at this keyboard to approve a
		// plan for a throwaway account, and the account is the only thing at stake.
		inner := f
		inner.claimableRelease = true
		inner.promote.yes = true
		// Every outcome is remembered beside the token, so `env status
		// --claimable` can tell a published deploy from one whose every
		// attempt failed: both have a claim link and an expiry. Publication
		// is recorded when the promotion is written, so a rollout that then
		// times out or degrades still reads as published.
		inner.promoted = session.markPublished
		inner.claim = func() *deployClaimJSON {
			// Asked for only once the deploy succeeded: its document says so.
			session.recordAttempt(nil)
			session.refreshURL(ctx)
			doc := session.document(compute)
			doc.APIURL = claimSiteAPIURL(entities, doc.URL)
			return doc
		}
		// A content-scan refusal at publish (the follow-through) is
		// translated before the promotion's document is written, so that
		// document carries the refusal's exit code and claim.
		inner.refusal = func(err error) (*deployClaimJSON, error) {
			r := claimableContentRefusalOf(envName, session.cred.Claimable.ClaimURL, err)
			if r == nil {
				return nil, nil
			}
			r.emitted = f.jsonOut
			if _, ok := cloud.ClaimRequiredOf(err); !ok {
				return nil, r
			}
			session.recordAttempt(err)
			return session.document(compute), r
		}
		docsBefore := jsonDocumentsWritten.Load()
		deployErr := runDeployEverything(ctx, envName, inner)
		session.recordAttempt(deployErr)
		if deployErr != nil {
			var r *claimableRefusal
			if !errors.As(deployErr, &r) {
				// Refused before the promotion (RecordBundle, at the build):
				// no document has been written yet.
				r = claimableContentRefusalOf(envName, session.cred.Claimable.ClaimURL, deployErr)
			}
			if r != nil {
				r.emitted = r.emitted || jsonDocumentsWritten.Load() != docsBefore
				if _, ok := cloud.ClaimRequiredOf(deployErr); ok {
					// The way forward is the claim link: give a --json
					// caller the claim beside the refusal, unless the
					// deploy already wrote its one document.
					if f.jsonOut && !r.emitted {
						report.setClaim(session.document(compute))
					}
					noteDeployClaim(ctx, session.document(compute))
				}
				return r
			}
			return claimableDeployError(envName, deployErr)
		}
		doc := session.document(compute)
		doc.APIURL = claimSiteAPIURL(entities, doc.URL)
		doc.ConsoleURL = deployConsole(ep.URL, project).Home(envName)
		noteDeployClaim(ctx, doc)
		if !f.jsonOut {
			printClaimBlock(os.Stdout, envName, doc)
		}
		return nil
	}()
	if err == nil {
		return nil
	}
	if !f.promote.planOnly && !serverHeard && !errors.Is(err, context.Canceled) {
		// Refused before the control plane recorded a deploy attempt: it
		// never hears of this one, so forge counts it. After the attempt
		// opened, the attempt's own close records the failure.
		if opened, phase := deployAttemptFrom(ctx).progress(); !opened {
			if phase == "build" {
				stage = refusedBuild
			}
			trackClaimableRefused(claimableRefusedReason(err, stage), shape)
		}
	}
	// A refusal raised on this path owes a --json caller its envelope. An error
	// from inside the deploy keeps the deploy's own reporting.
	var r *claimableRefusal
	if f.jsonOut && errors.As(err, &r) && !r.emitted {
		return refuseDeployReported(report, err)
	}
	return err
}

// claimablePreviewJSON is `--claimable --plan-only --json`'s document.
type claimablePreviewJSON struct {
	jsonEnvelope
	Env      string `json:"env"`
	Endpoint string `json:"endpoint"`
	// Eligible is the verdict: the env fits the claimable plan.
	Eligible bool `json:"eligible"`
	// Reasons are what does not fit, each with its fix where there is one.
	Reasons []string `json:"reasons,omitempty"`
	// Availability is the control plane's answer, when it gave one: whether a
	// start, and this env's compute, would be admitted right now. Informational:
	// it describes the platform, not the project.
	Availability *cloud.ClaimableAvailability `json:"availability,omitempty"`
	// AvailabilityError is why the question could not be answered.
	AvailabilityError string `json:"availability_error,omitempty"`
	// NoOutboundInternet is true when the env runs a service: unclaimed, it has
	// no outbound internet (see noOutboundNote). Absent for a static site.
	NoOutboundInternet bool `json:"no_outbound_internet,omitempty"`
	// BlockingCauses are the availability causes that would refuse THIS env.
	BlockingCauses []cloud.ClaimableCause `json:"blocking_causes,omitempty"`
	// SignedIn is true when a real credential resolves for the endpoint, in
	// which case `--claimable` itself is refused from this machine.
	SignedIn bool `json:"signed_in"`
	// Limits are the caps the env was judged by; LimitsPublished says whether
	// the control plane published them (false: forge's compiled-in copy,
	// with LimitsNote saying why).
	Limits          claimableLimitsJSON `json:"limits"`
	LimitsPublished bool                `json:"limits_published"`
	LimitsNote      string              `json:"limits_note"`
	// ReleaseRefusal is why the deploy would refuse to cut a release from
	// this checkout, judged before anything is built: today, a migrations
	// directory that cannot be read. (A migration the default branch does
	// not hold no longer is one: an anonymous deploy applies the working
	// tree's, claimableMigrationNote.) Empty when none. Not part of Eligible
	// (which is the env's fit to the plan), but it fails the preview all the
	// same: the deploy cannot succeed.
	ReleaseRefusal string `json:"release_refusal,omitempty"`
}

// claimableMigrations is what the preview needs to judge the checkout's
// migrations (claimableMigrationNote).
type claimableMigrations struct {
	projectDir string
}

// claimableLimitsJSON is the caps in the preview document.
type claimableLimitsJSON struct {
	WorkloadCPUMillicores      int64 `json:"workload_cpu_millicores"`
	WorkloadMemoryMiB          int64 `json:"workload_memory_mib"`
	MaxServices                int   `json:"max_services"`
	MaxReplicas                int   `json:"max_replicas"`
	MaxMigrationJobs           int   `json:"max_migration_jobs"`
	MaxDatabases               int   `json:"max_databases"`
	DatabaseCPUMillicores      int64 `json:"database_cpu_millicores"`
	DatabaseMemoryMiB          int64 `json:"database_memory_mib"`
	DatabaseStorageMiB         int64 `json:"database_storage_mib"`
	DatabaseDeclaredStorageGiB int32 `json:"database_declared_storage_gib"`
	DatabaseMaxConnections     int   `json:"database_max_connections"`
	MaxStaticSites             int   `json:"max_static_sites"`
}

func limitsJSON(l deploy.ClaimableLimits) claimableLimitsJSON {
	return claimableLimitsJSON{
		WorkloadCPUMillicores: l.WorkloadCPUMillicores, WorkloadMemoryMiB: l.WorkloadMemoryBytes >> 20,
		MaxServices: l.MaxServices, MaxReplicas: l.MaxReplicas, MaxMigrationJobs: l.MaxMigrationJobs,
		MaxDatabases: l.MaxDatabases, DatabaseCPUMillicores: l.DatabaseCPUMillicores,
		DatabaseMemoryMiB: l.DatabaseMemoryBytes >> 20, DatabaseStorageMiB: l.DatabaseStorageBytes >> 20,
		DatabaseDeclaredStorageGiB: l.DatabaseDeclaredStorageGiB, DatabaseMaxConnections: l.DatabaseMaxConnections,
		MaxStaticSites: l.MaxStaticSites,
	}
}

// previewClaimable is `forge env deploy <env> --claimable --plan-only`: the
// local eligibility preview. It judges the render under the claimable plan —
// the caps, the hosted plan's admission, the resource rule — and asks the
// control plane's public availability question. It uses no identity, solves
// no proof of work, builds nothing and creates nothing. The verdict (and the
// exit code) is the project's eligibility, and then the checkout's: a
// migration the release cut would refuse fails it too. Availability
// describes the platform and is reported beside it.
func previewClaimable(env string, e *KCLEntities, ep cloud.Endpoint, answer claimableAnswer, limits claimableLimitsSource, mig claimableMigrations, jsonOut bool) error {
	doc := claimablePreviewJSON{Env: env, Endpoint: ep.URL}
	doc.Limits, doc.LimitsPublished, doc.LimitsNote = limitsJSON(limits.limits), limits.published, limits.note()
	doc.Reasons = claimableAdmission(env, e, limits.limits)
	doc.Eligible = len(doc.Reasons) == 0
	refusal := claimableMigrationNote(progressWriter(jsonOut), mig.projectDir, e)
	if refusal != nil {
		doc.ReleaseRefusal = refusal.Error()
	}
	doc.NoOutboundInternet = needsCompute(e)
	if _, err := cloud.ResolveCredential("", ep); err == nil {
		doc.SignedIn = true
	}
	av, ok, err := answer.av, answer.ok, answer.err
	switch {
	case err != nil:
		doc.AvailabilityError = err.Error()
	case !ok:
		doc.AvailabilityError = "this control plane does not answer GetClaimableAvailability"
	default:
		doc.Availability = &av
		doc.BlockingCauses = availabilityCauses(av, true, needsCompute(e))
	}
	if !doc.Eligible {
		err := claimableIneligibleError(env, limits.limits, doc.Reasons)
		if refusal != nil && !jsonOut {
			// The fit is the verdict; the checkout's refusal is said too, so
			// the second preview after fixing the caps holds no surprise.
			fmt.Fprintf(os.Stderr, "also: %s\n", refusal.Error())
		}
		if jsonOut {
			doc.stamp(err)
			if eerr := emitJSONDocument(doc); eerr != nil {
				return eerr
			}
			var r *claimableRefusal
			if errors.As(err, &r) {
				r.emitted = true
			}
		}
		return err
	}
	if jsonOut {
		doc.stamp(refusal)
		if err := emitJSONDocument(doc); err != nil {
			return err
		}
		var r *claimableRefusal
		if errors.As(refusal, &r) {
			r.emitted = true
		}
		return refusal
	}
	fmt.Printf("Anonymous deploy preview of env %s at %s (checked locally; no identity used, nothing created)\n", env, ep.URL)
	fmt.Printf("  %s\n", limits.note())
	fmt.Printf("  ✓ fits the claimable plan: one service (at most %s) with its migration job, one database, one static site\n",
		limits.limits.WorkloadShape())
	if needsCompute(e) {
		fmt.Printf("  ! no outbound internet until claimed: the service serves requests and uses its database, but calls out of it fail (payments, LLMs, webhooks, outside DNS)\n")
	}
	switch {
	case doc.Availability == nil:
		fmt.Printf("  ? platform availability unknown: %s\n", doc.AvailabilityError)
	case len(doc.BlockingCauses) == 0:
		fmt.Printf("  ✓ the platform is taking anonymous deploys of this shape right now\n")
	default:
		fmt.Printf("  ✗ the platform is NOT taking anonymous deploys of this shape right now:\n")
		for _, c := range doc.BlockingCauses {
			fmt.Printf("      %-24s %s\n", claimableCauseLabel(c), strings.TrimSuffix(c.Message, "."))
		}
	}
	// Push access is a question about a credential, and this preview holds
	// none by design: the anonymous one exists only once the deploy mints it.
	// The deploy then asks the registry before it builds (registry_push_preflight.go).
	fmt.Printf("  ? registry push access: not checked here (no identity); the deploy checks it with its own credential before it builds\n")
	if doc.SignedIn {
		fmt.Printf("  note: you are signed in to %s, so --claimable is refused from this machine; drop it to deploy to your organization\n", ep.URL)
	}
	if refusal != nil {
		// The message itself is printed once, by the caller, as the error.
		fmt.Printf("  ✗ the deploy would refuse this checkout before building anything (below)\n")
		return refusal
	}
	fmt.Printf("\nDeploy it: %s env deploy %s --claimable\n", Name(), env)
	return nil
}

// refuseClaimableWhenSignedIn refuses --claimable when a real credential
// resolves: such a user deploys to their own organization.
func refuseClaimableWhenSignedIn(ep cloud.Endpoint, env string) error {
	cred, err := cloud.ResolveCredential("", ep)
	if err == nil {
		next := fmt.Sprintf("%s env deploy %s", Name(), env)
		msg := fmt.Sprintf("you are signed in to %s (%s: %s); drop --claimable to deploy to your organization: %s",
			ep.URL, cred.Source, cred.From, next)
		if escape := claimableEscape(cred.Source, ep.TokenEnv, env); escape != "" {
			msg += "\n" + escape
		}
		return &claimableRefusal{
			exit: exitWrong, code: cloud.CodeFailedPrecondition, reason: reasonClaimableSignedIn,
			msg:  msg,
			next: []string{next},
		}
	}
	if errors.Is(err, cloud.ErrNoCredential) {
		return nil
	}
	return err
}

// claimableEscape names how to deploy anonymously from this machine on
// purpose, given the source of the credential that refused --claimable (an
// agent reproducing a fresh user's first deploy on a machine that is signed
// in: obs 88c94bfa). The refusal itself stays: a signed-in user who passes
// --claimable by accident must not create an orphan preview. The command
// removes every credential this process can see, not only the one that
// refused, so it does not just move the refusal to the next source. "" for
// a --token flag (drop it). tokenEnv is the endpoint's declared token
// variable ("" means cloud.DefaultTokenEnv), the one ResolveCredential reads.
func claimableEscape(src cloud.CredentialSource, tokenEnv, env string) string {
	if src == cloud.SourceFlag {
		return ""
	}
	if tokenEnv == "" {
		tokenEnv = cloud.DefaultTokenEnv
	}
	args := []string{"env"}
	if os.Getenv("FORGE_CREDENTIAL_HELPER") != "" {
		args = append(args, "-u", "FORGE_CREDENTIAL_HELPER")
	}
	if os.Getenv(tokenEnv) != "" || src == cloud.SourceEnv {
		args = append(args, "-u", tokenEnv)
	}
	tail := ""
	if src == cloud.SourceLogin {
		args = append(args, "FORGE_HOME=<empty dir>")
		tail = " (keep that FORGE_HOME: the deploy's claim link and the token that stops or deletes it are stored there)"
	}
	return fmt.Sprintf("to deploy anonymously from this machine anyway, as a user with no account would: %s %s env deploy %s --claimable%s",
		strings.Join(args, " "), Name(), env, tail)
}

// claimableIneligibility lists what in the render an unclaimed deploy may not
// be (§2: "never"). The control plane enforces the same at admission; checking
// here saves a build and says it in the author's own vocabulary, with the KCL
// edit that fixes it where there is one. The resource rule is the claimable
// plan's (deploy.CheckHostedResources), the one the hosted plan applies too.
func claimableIneligibility(e *KCLEntities, caps deploy.ClaimableLimits) []string {
	var out []string
	var services, jobs int
	for _, w := range e.Workloads {
		if !w.OnRuntime(RuntimeHosted) {
			continue
		}
		switch w.Spec.EffectiveKind() {
		case v1alpha1.KindService:
			services++
		case v1alpha1.KindJob:
			if deploy.RunsBeforeRollout(w.Spec) {
				jobs++
			} else {
				out = append(out, fmt.Sprintf("job %q runs after the rollout (deployPhase = %q); only a migration that runs "+
					"before it is allowed: gate the service with `before = [fw.BEFORE_ALL]`, or drop the job here", w.Name, w.Spec.DeployPhase))
			}
		default:
			out = append(out, fmt.Sprintf("%s %q (only one service may run)", w.Spec.EffectiveKind(), w.Name))
		}
		if replicas := w.Spec.EffectiveMaxReplicas(); int(replicas) > caps.MaxReplicas && w.Spec.EffectiveKind() == v1alpha1.KindService {
			out = append(out, fmt.Sprintf("workload %q asks for %d replicas (%d is allowed)", w.Name, replicas, caps.MaxReplicas))
		}
		if w.Spec.EffectiveKind() == v1alpha1.KindService || w.Spec.EffectiveKind() == v1alpha1.KindJob {
			// The hosted shape band does not apply to this plan; these caps do.
			if err := deploy.CheckHostedResourcesWithin(deploy.HostedPlanClaimable, caps, w.Spec.Resources); err != nil {
				var r *deploy.ResourcesRefusal
				if errors.As(err, &r) {
					out = append(out, fmt.Sprintf("workload %q %s\n      fix: give it, in its binding (deploy/kcl/<env>/main.k): %s",
						w.Name, r.Reason, r.Fix()))
				} else {
					out = append(out, fmt.Sprintf("workload %q: %v", w.Name, err))
				}
			}
		}
		if w.Spec.StorageGiB > 0 {
			out = append(out, fmt.Sprintf("workload %q asks for a %d GiB persistent volume (use the database)", w.Name, w.Spec.StorageGiB))
		}
		if w.Build.Type == "remote" {
			out = append(out, fmt.Sprintf("workload %q uses a remote build (build and push the image from this machine)", w.Name))
		}
		for _, p := range w.Spec.Ports {
			if len(p.Domains) > 0 {
				out = append(out, fmt.Sprintf("workload %q declares custom domains", w.Name))
				break
			}
		}
	}
	if services > caps.MaxServices {
		out = append(out, fmt.Sprintf("%d services (%d is allowed)", services, caps.MaxServices))
	}
	if jobs > caps.MaxMigrationJobs || (jobs > 0 && services == 0) {
		out = append(out, "a job that gates no service (only a service's own migration job is allowed)")
	}
	var dbs, sites int
	for _, d := range e.Databases {
		if !d.Hosted() {
			continue
		}
		dbs++
		spec := d.Spec.WithDefaults()
		if int(spec.Instances) > caps.DatabaseInstances {
			out = append(out, fmt.Sprintf("database %q asks for %d instances (%d is allowed)", d.Name, spec.Instances, caps.DatabaseInstances))
		}
		if spec.StorageGiB > caps.DatabaseDeclaredStorageGiB {
			out = append(out, fmt.Sprintf("database %q asks for %d GiB of storage (at most %d GiB); the platform provisions %s\n"+
				"      fix: give it, in deploy/kcl/<env>/main.k: spec = {storageGiB = %d}",
				d.Name, spec.StorageGiB, caps.DatabaseDeclaredStorageGiB, caps.DatabaseStorage(), caps.DatabaseDeclaredStorageGiB))
		}
		if spec.Restore != nil {
			out = append(out, fmt.Sprintf("database %q restores from a backup (an anonymous database has none)", d.Name))
		}
	}
	for _, fe := range e.Frontends {
		if frontendIsHosted(fe) {
			sites++
		}
	}
	if dbs > caps.MaxDatabases {
		out = append(out, fmt.Sprintf("%d databases (%d is allowed)", dbs, caps.MaxDatabases))
	}
	if sites > caps.MaxStaticSites {
		out = append(out, fmt.Sprintf("%d static sites (%d is allowed)", sites, caps.MaxStaticSites))
	}
	sort.Strings(out)
	return out
}

// claimableAdmission is every local check of an anonymous deploy: the plan's
// structural caps (claimableIneligibility), then, if those pass, the hosted
// plan the deploy itself will run under the claimable plan (restricted
// profile, resources, references), so a refusal there also costs no build.
func claimableAdmission(env string, e *KCLEntities, caps deploy.ClaimableLimits) []string {
	if reasons := claimableIneligibility(e, caps); len(reasons) > 0 {
		return reasons
	}
	restore := useClaimablePreview()
	defer restore()
	group, err := buildHostedGroup(env, e)
	if err != nil {
		return []string{err.Error()}
	}
	if group == nil {
		return nil
	}
	if _, err := deploytarget.PreflightHosted(*group); err != nil {
		return []string{err.Error()}
	}
	return nil
}

func claimableIneligibleError(env string, caps deploy.ClaimableLimits, reasons []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "env %q does not fit an anonymous deploy. An unclaimed deploy is one static site, or one service "+
		"(at most %s, request and limit) with its migration job and one database (at most %d GiB declared); it cannot have:",
		env, caps.WorkloadShape(), caps.DatabaseDeclaredStorageGiB)
	for _, r := range reasons {
		fmt.Fprintf(&b, "\n  - %s", strings.ReplaceAll(r, "<env>", env))
	}
	fmt.Fprintf(&b, "\nfix those, then re-run: %s env deploy %s --claimable", Name(), env)
	fmt.Fprintf(&b, "\nor deploy it to your own account instead: forge login, then %s env deploy %s", Name(), env)
	return &claimableRefusal{
		exit: exitRefused, code: cloud.CodeFailedPrecondition, reason: reasonClaimableIneligible, msg: b.String(),
		next: []string{fmt.Sprintf("%s env deploy %s --claimable", Name(), env), loginThenDeploy(env)},
	}
}

// needsCompute reports whether the env runs anything but a static site under
// the claimable plan: a service, a job or a database.
func needsCompute(e *KCLEntities) bool {
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeHosted) {
			return true
		}
	}
	for _, d := range e.Databases {
		if d.Hosted() {
			return true
		}
	}
	return false
}

// claimableAnswer is one GetClaimableAvailability answer (claimableAvailabilityOf).
type claimableAnswer struct {
	av  cloud.ClaimableAvailability
	ok  bool
	err error
}

func askClaimableAvailability(ctx context.Context, ep cloud.Endpoint) claimableAnswer {
	av, ok, err := claimableAvailabilityOf(ctx, ep)
	return claimableAnswer{av: av, ok: ok, err: err}
}

// claimableAvailabilityOf asks the control plane whether this env could be
// deployed anonymously right now. ok is false when the question could not be
// answered (an older control plane, a network failure): the caller then lets
// the challenge and the start answer for themselves.
func claimableAvailabilityOf(ctx context.Context, ep cloud.Endpoint) (cloud.ClaimableAvailability, bool, error) {
	av, err := claimableAPI(ep).Availability(ctx)
	if err == nil {
		return av, true, nil
	}
	var ce *cloud.Error
	if errors.As(err, &ce) && ce.HasCode(cloud.CodeUnimplemented) {
		return cloud.ClaimableAvailability{}, false, nil
	}
	if errors.As(err, &ce) && ce.HTTPStatus == http.StatusNotFound {
		return cloud.ClaimableAvailability{}, false, nil
	}
	return cloud.ClaimableAvailability{}, false, err
}

// availabilityCauses are the causes that block THIS env: the start's when it
// must mint a new anonymous deploy (a redeploy of a stored one is not a
// start), plus compute's when the env runs a service or a database.
func availabilityCauses(av cloud.ClaimableAvailability, minting, compute bool) []cloud.ClaimableCause {
	var out []cloud.ClaimableCause
	if minting {
		out = append(out, av.Causes...)
	}
	if compute && !av.ComputeAvailable {
		out = append(out, av.ComputeCauses...)
	}
	return out
}

// requireClaimableAvailable is the preflight: before any proof of work, any
// account and any build, refuse an env the platform cannot take anonymously
// right now, saying why.
func requireClaimableAvailable(answer claimableAnswer, ep cloud.Endpoint, env string, e *KCLEntities, minting bool) error {
	av, ok, err := answer.av, answer.ok, answer.err
	if err != nil || !ok {
		return nil // unanswerable: the start reports for itself
	}
	if causes := availabilityCauses(av, minting, needsCompute(e)); len(causes) > 0 {
		return claimableUnavailableError(env, ep, causes)
	}
	return nil
}

// claimableUnavailableError renders why the platform cannot take an
// anonymous deploy, cause by cause, in the platform's own words. An anonymous
// deploy was asked for, so signing in is offered only as the alternative.
func claimableUnavailableError(env string, ep cloud.Endpoint, causes []cloud.ClaimableCause) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Claimable (anonymous) deploys are not available at %s for env %q. Nothing was created.", ep.URL, env)
	retry := false
	for _, c := range causes {
		fmt.Fprintf(&b, "\n  %-24s %s", claimableCauseLabel(c), strings.TrimSuffix(c.Message, "."))
		retry = retry || c.Code == cloud.ClaimableCauseDependencyUnavailable
	}
	var next []string
	if retry {
		b.WriteString("\n  This is the platform's configuration, not your project: retry once it is fixed.")
		next = append(next, fmt.Sprintf("%s env deploy %s --claimable", Name(), env))
	}
	fmt.Fprintf(&b, "\n  alternative: deploy to your own account instead: forge login, then %s env deploy %s", Name(), env)
	first := causes[0]
	return &claimableRefusal{
		exit: exitRefused, code: cloud.CodeUnavailable, reason: cloud.ReasonClaimableUnavailable,
		msg: b.String(), cause: &first, next: append(next, loginThenDeploy(env)),
	}
}

func claimableCauseLabel(c cloud.ClaimableCause) string {
	switch c.Code {
	case cloud.ClaimableCauseDisabled:
		return "disabled by operator:"
	case cloud.ClaimableCauseDependencyUnavailable:
		return "unavailable dependency (" + c.Component + "):"
	case cloud.ClaimableCauseNotSupported:
		return "not supported yet:"
	case cloud.ClaimableCauseNotDeployed:
		return "not deployed:"
	}
	return c.Code + ":"
}

// obtainClaimable returns this machine's anonymous deploy for (project, env):
// the stored one when the control plane still accepts its token, otherwise a
// fresh one from StartClaimableDeploy.
func obtainClaimable(ctx context.Context, out io.Writer, ep cloud.Endpoint, project, env string) (*claimableSession, error) {
	now := claimableNow()
	if stored, err := cloud.LoadClaimable(ep, project, env, now); err == nil {
		switch err := probeClaimableToken(ctx, ep, stored.Token); {
		case err == nil:
			fmt.Fprintf(out, "Reusing this machine's claimable deploy of %s/%s (goes offline %s)\n",
				project, env, stored.Claimable.ExpiresAt.UTC().Format(time.RFC3339))
			return &claimableSession{ep: ep, project: project, env: env, cred: stored, reused: true}, nil
		case isClaimedError(err):
			return nil, claimableClaimedError(env)
		case isDeadTokenError(err):
			// Expired or unknown to the control plane: the deploy is gone, so
			// mint a new one rather than fail on a token nobody accepts.
			_ = cloud.ForgetClaimable(ep, project, env)
		default:
			return nil, err
		}
	} else if !errors.Is(err, credentials.ErrNotFound) {
		return nil, err
	}

	api := claimableAPI(ep)
	deviceID, err := cloud.ClaimableDeviceID()
	if err != nil {
		return nil, err
	}
	var started cloud.ClaimableDeploy
	for attempt := 0; ; attempt++ {
		ch, err := api.Challenge(ctx)
		if err != nil {
			return nil, claimableStartError(ep, env, err)
		}
		fmt.Fprintf(out, "Requesting an anonymous deploy for %s/%s (solving a %d-bit proof of work)…\n", project, env, ch.DifficultyBits)
		nonce, err := cloud.SolvePoW(ctx, ch)
		if err != nil {
			return nil, err
		}
		started, err = api.Start(ctx, cloud.ClaimableStartRequest{
			Project: project, Env: env, DeviceID: deviceID, PoWChallenge: ch.Challenge, PoWNonce: nonce,
		})
		if err == nil {
			break
		}
		var ce *cloud.Error
		// A challenge that expired while we solved it is refused as a bad
		// proof of work. A fresh one is the right response, once.
		if attempt == 0 && errors.As(err, &ce) && ce.HasReason(cloud.ReasonClaimableInvalidPoW) {
			continue
		}
		return nil, claimableStartError(ep, env, err)
	}
	cred, err := cloud.StoreClaimable(ep, project, env, started, now)
	if err != nil {
		return nil, fmt.Errorf("the anonymous deploy was created but its token could not be saved (%w); "+
			"claim link: %s\n  nothing was published; fix the credential store, then re-run: %s env deploy %s --claimable",
			err, started.ClaimURL, Name(), env)
	}
	return &claimableSession{ep: ep, project: project, env: env, cred: cred, reused: started.Reused}, nil
}

// probeClaimableToken asks the control plane one cheap question with the
// stored token: is it still accepted?
func probeClaimableToken(ctx context.Context, ep cloud.Endpoint, token string) error {
	client := hostedDeployClient(ep, cloud.Credential{Token: token, Source: cloud.SourceClaimable, From: "stored claimable deploy"})
	_, err := cloud.ResolveOrganization(ctx, client)
	return err
}

func isClaimedError(err error) bool {
	var ce *cloud.Error
	return errors.As(err, &ce) && (ce.HasReason(cloud.ReasonClaimableClaimed) || ce.HasReason(cloud.ReasonAccessTokenRevoked))
}

func isDeadTokenError(err error) bool {
	var ce *cloud.Error
	return errors.As(err, &ce) && (ce.HasReason(cloud.ReasonAccessTokenExpired) || ce.HasReason(cloud.ReasonAccessTokenUnknown) ||
		ce.HasReason(cloud.ReasonClaimableExpired))
}

func claimableClaimedError(env string) error {
	return &claimableRefusal{
		exit: exitRefused, code: cloud.CodeFailedPrecondition, reason: cloud.ReasonClaimableClaimed,
		msg: fmt.Sprintf("this deploy was claimed: someone opened its claim link and it now belongs to their account.\n"+
			"  run forge login and deploy without --claimable: forge login, then %s env deploy %s", Name(), env),
		next: []string{loginThenDeploy(env)},
	}
}

// claimableDeployError translates what the ordinary deploy raised into this
// path's vocabulary: a claimed deploy, and a queued one (which an unclaimed org
// must never be: nobody could resolve the hold).
func claimableDeployError(env string, err error) error {
	if isClaimedError(err) {
		return claimableClaimedError(env)
	}
	if q := queuedOf(err); q != nil {
		return &claimableRefusal{
			exit: exitRefused, code: cloud.CodeFailedPrecondition, reason: reasonClaimableIneligible, emitted: true,
			msg: fmt.Sprintf("the control plane held this anonymous deploy on %s, which an unclaimed deploy cannot resolve.\n"+
				"sign up for the rest: forge login, then %s env deploy %s", queuedLabel(q.Holds), Name(), env),
			next: []string{loginThenDeploy(env)},
		}
	}
	return err
}

// claimableStartError renders a refusal of StartClaimableDeploy (or of its
// challenge).
func claimableStartError(ep cloud.Endpoint, env string, err error) error {
	if th, ok := cloud.ClaimableThrottleOf(err); ok {
		return newClaimableThrottle(env, th)
	}
	var ce *cloud.Error
	if errors.As(err, &ce) {
		switch {
		case ce.HasCode(cloud.CodeUnimplemented) || ce.HTTPStatus == http.StatusNotFound:
			return claimableUnavailableError(env, ep, []cloud.ClaimableCause{{Code: cloud.ClaimableCauseNotDeployed,
				Message: "this control plane does not serve claimable deploys (no ClaimableService)"}})
		case ce.HasReason(cloud.ReasonClaimableUnavailable):
			cause, ok := cloud.ClaimableUnavailableCauseOf(err)
			if !ok {
				// A control plane that names no cause: say what it said.
				cause = cloud.ClaimableCause{Code: "unavailable", Message: emptyAs(ce.Message, "the control plane refused anonymous deploys without saying why")}
			}
			return claimableUnavailableError(env, ep, []cloud.ClaimableCause{cause})
		case ce.HasReason(cloud.ReasonClaimableClaimed):
			return claimableClaimedError(env)
		case ce.HasReason(cloud.ReasonClaimableTakenDown), ce.HasReason(cloud.ReasonClaimableExpired):
			// Neither comes back anonymously: the platform took it down, or
			// its window closed. The deploy can go on under an account.
			return &claimableRefusal{exit: exitRefused, code: ce.Code, reason: ce.Reason, next: []string{loginThenDeploy(env)},
				msg: fmt.Sprintf("%s\n  (reason: %s)\n  deploy it to your own account instead: forge login, then %s env deploy %s",
					ce.Message, ce.Reason, Name(), env)}
		case ce.HasCode(cloud.CodeInvalidArgument), ce.HasCode(cloud.CodeFailedPrecondition):
			if ce.Reason != "" {
				return &claimableRefusal{exit: exitRefused, code: ce.Code, reason: ce.Reason, next: []string{loginThenDeploy(env)},
					msg: fmt.Sprintf("%s\n  (reason: %s)\n  or deploy it to your own account instead: forge login, then %s env deploy %s",
						ce.Message, ce.Reason, Name(), env)}
			}
		}
	}
	return err
}

func newClaimableThrottle(env string, th cloud.ClaimableThrottle) error {
	at := claimableNow().Add(th.RetryAfter).UTC()
	facts := &claimableThrottleFacts{scope: th.Scope, retryAfter: th.RetryAfter, retryAt: at}
	var why string
	switch th.Scope {
	case "platform":
		why = "too many new anonymous accounts\n  (platform-wide limit)"
	case "network":
		why = "too many claimable deploys from your network"
	case "device":
		why = "this machine already has 3 live claimable deploys"
	default:
		why = "too many claimable deploys right now"
	}
	var b strings.Builder
	// The commands come last: an agent acts on the end of the message.
	fmt.Fprintf(&b, "Claimable deploys are throttled right now: %s. Nothing was created.\n", why)
	b.WriteString("  (exit 4: throttled, not a failure of your project)\n")
	if th.RetryAfter > 0 {
		fmt.Fprintf(&b, "  retry after  %s  (at %s)\n", formatRetryAfter(th.RetryAfter), at.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "  then         %s env deploy %s --claimable\n", Name(), env)
	fmt.Fprintf(&b, "  or now       forge login, then %s env deploy %s (your own free account; this limit does not apply)", Name(), env)
	return &claimableRefusal{
		exit: exitRefused, code: cloud.CodeResourceExhausted, reason: cloud.ReasonClaimableThrottled,
		msg: b.String(), throttle: facts,
		next: []string{fmt.Sprintf("%s env deploy %s --claimable", Name(), env), loginThenDeploy(env)},
	}
}

// loginThenDeploy is the one command line that leaves the anonymous path: sign
// in, then deploy env to the account.
func loginThenDeploy(env string) string {
	return fmt.Sprintf("forge login && %s env deploy %s", Name(), env)
}

// formatRetryAfter renders a wait the way an agent can act on: "45s", "14m",
// "1h5m". Minutes round UP, so the agent never comes back too early.
func formatRetryAfter(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	mins := int((d + time.Minute - 1) / time.Minute)
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	if mins%60 == 0 {
		return fmt.Sprintf("%dh", mins/60)
	}
	return fmt.Sprintf("%dh%dm", mins/60, mins%60)
}

// ─── Output ──────────────────────────────────────────────────────────────────

// refreshURL reads the deploy's live address from the control plane and keeps
// it beside the token, so `env status --claimable` can print it later with no
// network.
func (s *claimableSession) refreshURL(ctx context.Context) {
	client := hostedDeployClient(s.ep, cloud.Credential{Token: s.cred.Token, Source: cloud.SourceClaimable, From: "claimable deploy"})
	st, err := deploytarget.ReadHostedStatus(ctx, client, s.project, s.env)
	if err != nil {
		return
	}
	if url := primaryURL(st.Workloads); url != "" {
		s.cred.Claimable.URL = url
		_ = cloud.RecordClaimableURL(s.ep, s.project, s.env, url)
	}
}

// markPublished records that this deploy's promotion was written: from now
// on the claimable deploy has something behind it, whatever the rollout does.
// Durable at once, so a deploy that then fails or is killed still reads as
// published.
func (s *claimableSession) markPublished() {
	s.recordAttempt(nil)
}

// recordAttempt sets how this deploy ended, in memory and beside the token:
// published (deployErr nil), or the error, keeping an earlier publication.
func (s *claimableSession) recordAttempt(deployErr error) {
	at := claimableNow().UTC()
	cl := s.cred.Claimable
	cl.LastAttemptAt, cl.LastAttemptError, cl.LastAttemptClaimRequired = at, "", ""
	if deployErr == nil {
		cl.LastPublishedAt = at
	} else {
		cl.LastAttemptError = cloud.FirstErrorLine(deployErr)
		cl.LastAttemptClaimRequired, _ = cloud.ClaimRequiredOf(deployErr)
	}
	_ = cloud.RecordClaimableAttempt(s.ep, s.project, s.env, at, deployErr)
}

// primaryURL is the address to show first: the static site's when there is one,
// else the first workload that has one.
func primaryURL(ws []deploytarget.HostedWorkloadStatus) string {
	first := ""
	for _, w := range ws {
		if w.URL == "" {
			continue
		}
		if w.Tier == "static" {
			return w.URL
		}
		if first == "" {
			first = w.URL
		}
	}
	return first
}

// document is the session as the --json `claim` object.
func (s *claimableSession) document(compute bool) *deployClaimJSON {
	return claimDocument(s.env, s.cred, claimableNow(), compute)
}

func claimDocument(env string, c credentials.Credential, now time.Time, compute bool) *deployClaimJSON {
	cl := c.Claimable
	state := "live"
	switch {
	case !cl.PurgeAfter.IsZero() && !now.Before(cl.PurgeAfter):
		state = "expired"
	case !cl.ExpiresAt.IsZero() && !now.Before(cl.ExpiresAt):
		state = "suspended"
	}
	published := cl.Published()
	instr := fmt.Sprintf("Give claim_url to your user and tell them it expires at %s. "+
		"Opening it signs them up for free and makes this deploy theirs. "+
		"It is a one-time link for your user: whoever claims it first owns the deploy, so show it only to them, "+
		"never in a commit, a log, an issue or anywhere public. "+
		"To see it again: %s env status %s --claimable",
		cl.ExpiresAt.UTC().Format(time.RFC3339), Name(), env)
	// The newest attempt's cause (it is reset on every attempt): set while a
	// claim is what the deploy waits on, whether or not an earlier release
	// published.
	claimRequired := cl.LastAttemptClaimRequired
	switch {
	case claimRequired != "" && claimableState(state):
		instr = claimRequiredInstructions(env, cl)
	case !published:
		instr = nothingPublishedInstructions(env, cl)
	case claimableState(state):
		instr += ". " + afterClaimNote(env)
	}
	if compute {
		instr += ". " + noOutboundNote
	}
	return &deployClaimJSON{
		State: state, Published: published, LastAttempt: lastAttemptOf(cl), ClaimRequired: claimRequired,
		URL: cl.URL, ClaimURL: cl.ClaimURL,
		ExpiresAt: cl.ExpiresAt, PurgeAfter: cl.PurgeAfter,
		AgentInstructions: instr, NoOutboundInternet: compute,
	}
}

// nothingPublishedInstructions is agent_instructions for a claimable deploy
// with nothing behind it. Claiming it now would hand the human an empty
// account and revoke the token this machine deploys with, so the link is
// held back until a deploy succeeds.
func nothingPublishedInstructions(env string, cl *credentials.Claimable) string {
	why := "no deploy of it has published yet"
	if cl.LastAttemptError != "" {
		why = "every deploy so far failed; the last one: " + cl.LastAttemptError
	}
	return fmt.Sprintf("Nothing is deployed: %s. Do NOT give claim_url to your user yet: claiming now signs them up "+
		"for an empty deploy and revokes the token this machine deploys with. Fix the failure and deploy again: "+
		"%s env deploy %s --claimable. Its clock started with the first attempt and a redeploy does not extend it: "+
		"it goes offline at %s",
		why, Name(), env, cl.ExpiresAt.UTC().Format(time.RFC3339))
}

// claimRequiredInstructions is agent_instructions for a claimable deploy the
// platform publishes only once it is claimed. Nothing is behind the link yet,
// but claiming is the way forward, so the link is given out.
func claimRequiredInstructions(env string, cl *credentials.Claimable) string {
	return fmt.Sprintf("Nothing is deployed yet: the platform publishes this deploy's content only once the deploy is "+
		"claimed. Claim to unlock: give claim_url to your user and tell them it expires at %s; opening it signs them up "+
		"for free and makes this deploy theirs. It is a one-time link for your user: whoever claims it first owns the "+
		"deploy, so show it only to them, never in a commit, a log, an issue or anywhere public. "+
		"Once claimed, they sign in on this machine and deploy: %s",
		cl.ExpiresAt.UTC().Format(time.RFC3339), loginThenDeploy(env))
}

// claimContentRefusal renders a content refusal that claiming lifts: what
// the platform refused, the claim link, and the ways forward. Exit 4, as for
// every content refusal (contentscan's contract).
func claimContentRefusal(env, claimURL string, deployErr error) *claimableRefusal {
	var b strings.Builder
	fmt.Fprintf(&b, "the platform publishes env %q only once its deploy is claimed. Nothing was published.\n", env)
	var ce *cloud.Error
	if errors.As(deployErr, &ce) && ce.Message != "" {
		fmt.Fprintf(&b, "  platform: %s\n", ce.Message)
	}
	b.WriteString(claimToUnlockNote + "\n")
	fmt.Fprintf(&b, "\n  Claim:  %s\n\n", claimURL)
	b.WriteString("  FOR AN AGENT: give the claim link above to your user. Opening it signs them up for free and makes\n")
	b.WriteString("  this deploy theirs. It is a one-time link for your user: whoever claims it first owns the\n")
	b.WriteString("  deploy, so show it only to them, never in a commit, a log, an issue or anywhere public.\n")
	fmt.Fprintf(&b, "  fix: give the claim link to your user; once claimed, they sign in on this machine and deploy: %s",
		loginThenDeploy(env))
	r := &claimableRefusal{
		exit: exitRefused, code: cloud.CodeFailedPrecondition, reason: cloud.ReasonClaimableContentRefused,
		msg: b.String(), next: []string{loginThenDeploy(env)}, platformErr: deployErr,
	}
	if ce != nil && ce.Code != "" {
		r.code = ce.Code
	}
	return r
}

// claimableContentRefusalOf is the claimable path's rendering of a content
// scan refusal of the upload, or nil when err is none.
func claimableContentRefusalOf(env, claimURL string, err error) *claimableRefusal {
	if _, ok := cloud.ClaimRequiredOf(err); ok {
		return claimContentRefusal(env, claimURL, err)
	}
	return claimableScanRefusal(env, err)
}

// claimableScanRefusal renders the platform content scan's other answers to
// an anonymous deploy's upload: a refusal claiming does not lift
// (claimable_content_refused with no claim-required header), and a scan that
// could not answer (claimable_scan_unavailable). Both exit 4 (refused,
// nothing lost), name the reason and the evidence id support needs, and give
// the next command; a --json caller gets the envelope. nil for any other
// error.
func claimableScanRefusal(env string, deployErr error) *claimableRefusal {
	var ce *cloud.Error
	if !errors.As(deployErr, &ce) {
		return nil
	}
	evidence := ""
	if ce.Header != nil {
		if id := strings.TrimSpace(ce.Header.Get("x-evidence-id")); id != "" {
			evidence = fmt.Sprintf("\n  evidence id: %s (quote it to Reliant support if you think this is wrong)", id)
		}
	}
	retry := fmt.Sprintf("%s env deploy %s --claimable", Name(), env)
	switch {
	case ce.HasReason(cloud.ReasonClaimableScanUnavailable):
		return &claimableRefusal{
			exit: exitRefused, code: emptyAs(ce.Code, cloud.CodeUnavailable), reason: ce.Reason, next: []string{retry}, platformErr: deployErr,
			msg: fmt.Sprintf("the platform's content scan could not judge this anonymous deploy's upload right now. Nothing was published.\n"+
				"  platform: %s%s\n  fix: retry in a few minutes: %s", emptyAs(ce.Message, "the scan is unavailable"), evidence, retry),
		}
	case ce.HasReason(cloud.ReasonClaimableContentRefused):
		return &claimableRefusal{
			exit: exitRefused, code: emptyAs(ce.Code, cloud.CodeFailedPrecondition), reason: ce.Reason, next: []string{loginThenDeploy(env)}, platformErr: deployErr,
			msg: fmt.Sprintf("the platform's content scan refused this anonymous deploy's upload, and claiming would not change that. "+
				"Nothing was published.\n  platform: %s%s\n"+
				"  An organization's own deploys are not scanned this way.\n  fix: deploy it to your own account: %s",
				emptyAs(ce.Message, "the content was refused"), evidence, loginThenDeploy(env)),
		}
	}
	return nil
}

// claimToUnlockNote is the one sentence that says what claiming unlocks, in
// every place a claim-required refusal is explained.
const claimToUnlockNote = "  Claim to unlock: the platform publishes this content only for a claimed deploy."

// lastAttemptOf is the stored outcome of the newest deploy, or nil when this
// machine recorded none (an entry written by an older forge).
func lastAttemptOf(cl *credentials.Claimable) *claimAttemptJSON {
	if cl.LastAttemptAt.IsZero() {
		return nil
	}
	a := &claimAttemptJSON{At: cl.LastAttemptAt, Outcome: "published"}
	if cl.LastAttemptError != "" {
		a.Outcome, a.Error = "failed", cl.LastAttemptError
	}
	return a
}

// printNothingPublished renders a claimable deploy with nothing behind it:
// say so, say why when it is known, and keep the claim link back.
func printNothingPublished(w io.Writer, env string, c *deployClaimJSON) {
	now := claimableNow()
	if c.ClaimRequired != "" && claimableState(c.State) {
		printClaimRequired(w, env, c)
		return
	}
	fmt.Fprintf(w, "\n✗ Nothing is deployed: no deploy of %s has published yet.\n", env)
	if a := c.LastAttempt; a != nil && a.Error != "" {
		fmt.Fprintf(w, "  The last attempt failed %s: %s\n", a.At.UTC().Format("2006-01-02 15:04 MST"), a.Error)
	}
	fmt.Fprintf(w, "  fix it, then deploy again: %s env deploy %s --claimable\n", Name(), env)
	if c.State == "suspended" {
		fmt.Fprintf(w, "\n  Its unclaimed window closed %s; it is deleted %s.\n",
			c.ExpiresAt.UTC().Format("2006-01-02 15:04 MST"), c.PurgeAfter.UTC().Format("2006-01-02 15:04 MST"))
	} else {
		fmt.Fprintf(w, "\n  Its clock started with the first attempt: it goes offline %s (in %s)\n",
			c.ExpiresAt.UTC().Format("2006-01-02 15:04 MST"), formatLifetime(c.ExpiresAt.Sub(now)))
		fmt.Fprintf(w, "  whether or not a deploy succeeds by then; deploying again does not extend it.\n")
	}
	fmt.Fprintf(w, "\n  FOR AN AGENT: do not give the claim link to your user yet. Claiming now signs\n")
	fmt.Fprintf(w, "  them up for an empty deploy and revokes the token this machine deploys with.\n")
	fmt.Fprintf(w, "  Once a deploy succeeds: %s env status %s --claimable\n", Name(), env)
}

// printClaimRequired renders a claimable deploy the platform publishes only
// once it is claimed: nothing is live, and the claim link is the way forward.
func printClaimRequired(w io.Writer, env string, c *deployClaimJSON) {
	now := claimableNow()
	fmt.Fprintf(w, "\n✗ Nothing is deployed: the platform publishes %s only once this deploy is claimed.\n", env)
	fmt.Fprintf(w, "%s\n", claimToUnlockNote)
	fmt.Fprintf(w, "\n  Claim:  %s\n", c.ClaimURL)
	fmt.Fprintf(w, "\n  FOR AN AGENT: give the claim link above to your user and tell them it expires in %s\n",
		formatLifetime(c.ExpiresAt.Sub(now)))
	fmt.Fprintf(w, "  (%s). Opening it signs them up for free and makes this deploy theirs. It is a\n",
		c.ExpiresAt.UTC().Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(w, "  one-time link for your user: whoever claims it first owns the deploy, so show it\n")
	fmt.Fprintf(w, "  only to them, never in a commit, a log, an issue or anywhere public.\n")
	fmt.Fprintf(w, "  Once claimed, they sign in on this machine and deploy: %s\n", loginThenDeploy(env))
}

// printClaimBlock renders the claim for a person or an agent reading text. The
// wording is §6 of the design; the agent line is the point of the whole block.
func printClaimBlock(w io.Writer, env string, c *deployClaimJSON) {
	if !c.Published {
		printNothingPublished(w, env, c)
		return
	}
	now := claimableNow()
	if c.State == "live" || c.State == "suspended" {
		if c.URL != "" {
			fmt.Fprintf(w, "\n✓ Live:   %s\n", c.URL)
			if c.APIURL != "" {
				fmt.Fprintf(w, "  API:    %s\n", c.APIURL)
			}
		}
	}
	left := c.ExpiresAt.Sub(now)
	fmt.Fprintf(w, "\n  This deploy belongs to no account. It goes offline %s (in %s)\n",
		c.ExpiresAt.UTC().Format("2006-01-02 15:04 MST"), formatLifetime(left))
	fmt.Fprintf(w, "  and is deleted %s after that, unless someone claims it.\n", formatLifetime(c.PurgeAfter.Sub(c.ExpiresAt)))
	fmt.Fprintf(w, "\n  Claim:  %s\n", c.ClaimURL)
	fmt.Fprintf(w, "\n  FOR AN AGENT: give the claim link above to your user and tell them it expires in %s.\n", formatLifetime(left))
	fmt.Fprintf(w, "  Opening it signs them up for free and makes this site theirs.\n")
	fmt.Fprintf(w, "  It is a one-time link for your user: whoever claims it first owns the site, so show\n")
	fmt.Fprintf(w, "  it only to them, never in a commit, a log, an issue or anywhere public.\n")
	fmt.Fprintf(w, "  To see it again: %s env status %s --claimable\n", Name(), env)
	if c.NoOutboundInternet {
		fmt.Fprintf(w, "\n  NO OUTBOUND INTERNET until claimed: this backend serves requests and uses its own\n")
		fmt.Fprintf(w, "  database, but every call out of it (a payment, an LLM, a webhook, an outside DNS\n")
		fmt.Fprintf(w, "  name) fails with a connection error or an unresolvable name. That is the platform,\n")
		fmt.Fprintf(w, "  not a bug in the app. It ends when the deploy is claimed: the platform restarts\n")
		fmt.Fprintf(w, "  the backend on the normal hosted network.\n")
	}
	if claimableState(c.State) {
		printAfterClaim(w, env, c.ConsoleURL)
	}
}

// claimableState reports whether a deploy in state can still be claimed: live,
// or suspended (offline, not yet deleted). What the claimer gets is said only
// then; a claimed or expired deploy has no claim left to make.
func claimableState(state string) bool { return state == "live" || state == "suspended" }

// printAfterClaim tells the agent what to pass on about owning the deploy:
// where it is managed, what the free plan covers, and how to redeploy once
// this machine's claimable credential is revoked (afterClaimNote).
func printAfterClaim(w io.Writer, env, consoleURL string) {
	fmt.Fprintf(w, "\n  ONCE CLAIMED it is theirs, in their own Reliant account (free to sign up).\n")
	if consoleURL != "" {
		fmt.Fprintf(w, "  Its releases, logs and dashboards: %s (signed in)\n", consoleURL)
	}
	fmt.Fprintf(w, "  Free static hosting covers one organization per person, with no limit on its sites;\n")
	fmt.Fprintf(w, "  a backend or database needs hosting billing on their account.\n")
	fmt.Fprintf(w, "  The claim revokes this machine's claimable credential. To redeploy as them:\n")
	fmt.Fprintf(w, "  %s login, then %s env deploy %s\n", Name(), Name(), env)
}

// formatLifetime renders a span as hours (72h, 41h) up to three days, days
// beyond, minutes under an hour.
func formatLifetime(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int((d+time.Minute-1)/time.Minute))
	}
	hours := int((d + time.Hour - 1) / time.Hour)
	if hours > 72 && hours%24 == 0 {
		return fmt.Sprintf("%d days", hours/24)
	}
	return fmt.Sprintf("%dh", hours)
}

// runClaimableStatus is `forge env status <env> --claimable`: the claim link
// again, from the stored entry, with no credential.
func runClaimableStatus(ctx context.Context, env string, jsonOut bool) error {
	entities, err := RenderKCL(ctx, projectDirForKCL(), env)
	if err != nil {
		return err
	}
	if entities == nil || entities.ControlPlane == nil {
		return fmt.Errorf("env %q declares no hosted control plane, so it has no claimable deploy", env)
	}
	ep, err := cloud.ResolveEndpoint(env, declarationFromEntities(entities))
	if err != nil {
		return err
	}
	project := hostedProjectName()
	now := claimableNow()
	stored, err := cloud.LoadClaimable(ep, project, env, now)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return fmt.Errorf("this machine has no claimable deploy of %s/%s at %s (none made, or it was deleted).\n"+
				"fix: %s env deploy %s --claimable", project, env, ep.URL, Name(), env)
		}
		return err
	}
	perr := probeClaimableToken(ctx, ep, stored.Token)
	if perr == nil {
		// The live address, when the platform has one: a deploy that went
		// live after its run ended, or the proof that something is.
		session := &claimableSession{ep: ep, project: project, env: env, cred: stored}
		session.refreshURL(ctx)
		stored = session.cred
	}
	doc := claimDocument(env, stored, now, needsCompute(entities))
	doc.APIURL = claimSiteAPIURL(entities, doc.URL)
	if isClaimedError(perr) {
		doc.State = "claimed"
	}
	if jsonOut {
		out := struct {
			jsonEnvelope
			Env   string           `json:"env"`
			Claim *deployClaimJSON `json:"claim"`
		}{Env: env, Claim: doc}
		out.stamp(nil)
		return emitJSONDocument(out)
	}
	if doc.State == "claimed" {
		fmt.Printf("This deploy was claimed: it belongs to someone's account now.\n"+
			"  run forge login and deploy without --claimable: forge login, then %s env deploy %s\n", Name(), env)
		return nil
	}
	if doc.State == "expired" {
		fmt.Printf("This claimable deploy was deleted on %s.\n  deploy again: %s env deploy %s --claimable\n",
			doc.PurgeAfter.UTC().Format(time.RFC3339), Name(), env)
		return nil
	}
	if doc.State == "suspended" && doc.Published {
		fmt.Printf("\nThis deploy is OFFLINE since %s and is deleted on %s unless someone claims it.\n",
			doc.ExpiresAt.UTC().Format(time.RFC3339), doc.PurgeAfter.UTC().Format(time.RFC3339))
		fmt.Printf("\n  Claim:  %s\n\n  FOR AN AGENT: give the claim link above to your user.\n", doc.ClaimURL)
		return nil
	}
	printClaimBlock(os.Stdout, env, doc)
	return nil
}
