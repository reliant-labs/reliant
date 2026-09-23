// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

func init() {
	RegisterCommand("forge.deploy_plan", handleForgeDeployPlan)
	RegisterCommand("forge.deploy_start", handleForgeDeployStart)
	RegisterCommand("forge.deploy_status", handleForgeDeployStatus)
}

// =============================================================================
// forge.deploy_* — THE ONLY PATH IN THIS FAMILY THAT MUTATES A LIVE CLUSTER.
//
// Everything in cmd_forge.go reads. cmd_forge_promote.go writes ONE file in the
// repo. This writes to Kubernetes, and the blast radius is not recoverable from
// git: control-plane's prod env declares a live GKE production cluster. So the
// shape here is deliberately more conservative than promote's, in three ways.
//
// 1. THREE COMMANDS, NOT TWO, BECAUSE AN APPLY CANNOT BE SYNCHRONOUS.
//
// A single forge invocation on the read path is capped at two minutes
// (forgeInvocationTimeout, shared by the five read commands). `forge env deploy`
// defaults to rollout mode `wait` with a FIVE-MINUTE budget PER RESOURCE, and
// control-plane's prod declares fourteen deployments. A real apply therefore
// routinely outlives two minutes, and a synchronous RPC would have the child
// KILLED MID-ROLLOUT: a half-converged cluster, plus an RPC reporting a timeout
// while the apply in fact landed. Nobody would know what shipped — the worst
// available outcome.
//
// The two tempting fixes are both wrong. Lowering forge's rollout budget to fit
// the cap makes forge stop waiting before Kubernetes has converged, which
// manufactures exactly the timed_out-reported-as-something-else problem forge's
// RolloutPolicy exists to avoid. Raising forgeInvocationTimeout changes the
// budget for five read commands that have no need of it, so a wedged topology
// read would hang for an hour.
//
// So the apply DETACHES: deploy_start returns a handle immediately and
// deploy_status polls it. See the background-job note below for which mechanism
// and why.
//
// 2. THE PREVIEW AND THE APPLY ARE SEPARATE COMMANDS, NEVER ONE WITH A FLAG.
//
// Same reasoning as promote, and it matters more here: a boolean defaults to
// false, and false is the destructive value. A caller that forgets the field, a
// proto that gains it after a client was written, a payload that loses it in
// transit — each turns the SAFE call into a production deploy. Three distinct
// command names cannot fail that way; the destructive one is reachable only by
// naming it.
//
// 3. TWO ESCAPE HATCHES ARE DELIBERATELY UNREACHABLE FROM HERE.
//
// `--skip-preflight` and `--no-digest` are not exposed and must never be. The
// preflight verifies that referenced Secret keys and container images actually
// exist on the LIVE target before anything is applied; digest pinning stops a
// re-tagged or node-cached layer from shipping in place of the bytes that were
// built and scanned. Both flags exist for a human at a terminal who has
// considered the consequence. An RPC field for either is a footgun: it would be
// set once by a caller working around an unrelated problem and then stay set.
//
// The transport rule from cmd_forge.go holds throughout: forge's JSON document
// crosses this layer as json.RawMessage, untouched. In particular NOTHING here
// reads or reinterprets a rollout state. forge reports ready / failed /
// timed_out / not_waited, where the last two are the ABSENCE of an answer, and
// this package has no code that could collapse one of them into success.
// =============================================================================

// -----------------------------------------------------------------------------
// WHICH BACKGROUND MECHANISM, AND WHY NOT THE EXISTING REGISTRY.
//
// The daemon already has a background-process lifecycle: exec.bg_start /
// bg_output / bg_kill / bg_list over shell.BackgroundManager (cmd_exec.go). It
// is a good registry — detached, no timeout, grant-scoped, output-buffered — and
// reusing it was the first thing tried. It was rejected on two grounds, the
// first of which is disqualifying:
//
//  1. IT HAS NO RUNNER SEAM, AND THIS FEATURE CANNOT BE TESTED WITHOUT ONE.
//     BackgroundManager.StartProcess builds an exec.Cmd and spawns it. There is
//     no indirection to substitute, and shell is not a package this work owns.
//     A deploy job routed through it could therefore only be tested by
//     spawning a REAL forge against a REAL project — and `forge env deploy`
//     without --dry-run/--explain mutates a live cluster. The whole forge.*
//     family is built on the forgeCommandRunner seam precisely so its tests
//     assert against canned bytes and never touch a cluster; a deploy path that
//     abandoned that seam would be the one untested command in the family, and
//     the most dangerous one.
//
//  2. ITS STATE MODEL ANSWERS A DIFFERENT QUESTION. A BackgroundProcess is
//     running / completed / failed / killed, derived from the exit status. What
//     a deploy caller needs is whether a REPORT exists and whether the outcome
//     is determinate — and for an apply, "the process died and produced no
//     report" must read as UNKNOWN, not as failed, because bytes may already
//     have reached the cluster. Mapping that onto exit status after the fact
//     would put the safety-critical judgment in the consumer.
//
// So this file keeps a small registry of its own: a map of handle -> job, each
// job a goroutine that calls the runForgeDeploy seam and records a terminal
// state. It is deliberately NOT a general-purpose process registry — it starts
// exactly one argv shape, holds no pipes, and its four states are about the
// deploy report rather than about a process. The duplication is ~80 lines and it
// buys a fully testable destructive path.
//
// The two registries do not overlap in purpose and neither is a generalisation
// of the other, so there is nothing here to unify later.
// -----------------------------------------------------------------------------

// forgeDeployInvocationTimeout is the HARD ceiling on one detached deploy.
//
// Distinct from forgeInvocationTimeout on purpose: that constant bounds the five
// synchronous read commands and must not be stretched to accommodate this one.
// This budget exists only so a wedged deploy cannot pin a goroutine and a
// handle forever — it is NOT a rollout budget, and forge's own per-resource
// budget should always expire first. Sized well above the worst realistic case
// (fourteen deployments at a five-minute per-resource budget, converging
// concurrently) so that hitting it means something is genuinely stuck.
const forgeDeployInvocationTimeout = 90 * time.Minute

// forgeDeployJobRetention is how long a FINISHED job's report stays readable.
// A caller polls for minutes, not hours, but a report that vanished the instant
// it was read once would lose the record of what shipped.
const forgeDeployJobRetention = 6 * time.Hour

// --- job lifecycle -----------------------------------------------------------

// THE JOB'S OWN LIFECYCLE IS NOT THE DEPLOY'S VERDICT, AND CONFLATING THEM IS
// THE MISTAKE THIS SPLIT EXISTS TO PREVENT.
//
// These four states answer one question: did the detached invocation reach a
// determinate outcome? Whether the DEPLOY succeeded is a separate question,
// answered by forge's report (ok, exit_code, rollout.results) which is carried
// verbatim alongside. A caller must read both. In particular
// forgeDeployJobStatusCompleted means "forge ran to completion and produced a
// report" — it never means the deploy worked, and a report with ok:false or a
// timed_out rollout arrives under exactly that status.
const (
	// forgeDeployJobStatusRunning: forge is still executing. NOT a success
	// and NOT a failure; the apply is in flight and its outcome does not
	// exist yet.
	forgeDeployJobStatusRunning = "running"

	// forgeDeployJobStatusCompleted: forge exited and produced a parseable
	// report. The report — not this status — says whether the deploy worked.
	forgeDeployJobStatusCompleted = "completed"

	// forgeDeployJobStatusFailed: the invocation never got off the ground,
	// so NOTHING can have been applied. Reserved for exactly that: forge
	// could not be started, or the pinned forge does not understand the
	// command. A process that started and then died is UNKNOWN, not this.
	forgeDeployJobStatusFailed = "failed"

	// forgeDeployJobStatusUnknown: the invocation started and ended without
	// a determinable outcome — killed by the ceiling above, killed by a
	// signal, the daemon restarted and lost the handle, or forge exited
	// without emitting a parseable document. Manifests may or may not have
	// reached the cluster.
	//
	// THIS IS THE STATE THAT MUST NOT BE COLLAPSED. Reading it as success
	// reports a deploy that may have half-landed as healthy; reading it as
	// failure invites a retry against a cluster that may already be
	// converging. The only correct consumer behaviour is to go and look.
	forgeDeployJobStatusUnknown = "unknown"
)

// forgeDeployJob is one detached deploy.
//
// Every field is read under mu because the goroutine writes the terminal state
// while a poller reads it.
type forgeDeployJob struct {
	mu sync.Mutex

	handle      string
	env         string
	projectPath string
	// grantID is the connector grant that started this job, empty for a
	// first-party caller. Handles are uuids rather than guessable ids, but
	// ownership is still checked on every poll — the same stance
	// exec.bg_output takes, for the same reason: a deploy report names
	// clusters, namespaces and image digests.
	grantID string

	startedAt  time.Time
	finishedAt time.Time

	status       string
	statusDetail string

	// report is forge's document, verbatim and unparsed.
	report json.RawMessage
	// exitCode is forge's exit status once it has one.
	exitCode int

	supported         bool
	unsupportedReason string
}

// snapshot copies the mutable state for a reply.
func (j *forgeDeployJob) snapshot() forgeDeployStatusResponse {
	j.mu.Lock()
	defer j.mu.Unlock()

	resp := forgeDeployStatusResponse{
		forgeResponseMeta: forgeResponseMeta{
			IsForgeProject:    true,
			Supported:         j.supported,
			ForgeVersion:      version.Forge(),
			UnsupportedReason: j.unsupportedReason,
			ExitCode:          j.exitCode,
		},
		Handle:          j.handle,
		Env:             j.env,
		JobStatus:       j.status,
		JobStatusDetail: j.statusDetail,
		StartedAt:       j.startedAt.UTC().Format(time.RFC3339),
		Report:          j.report,
	}
	if !j.finishedAt.IsZero() {
		resp.FinishedAt = j.finishedAt.UTC().Format(time.RFC3339)
	}
	return resp
}

// finish records the terminal state exactly once.
func (j *forgeDeployJob) finish(status, detail string, exitCode int, report json.RawMessage, supported bool, unsupportedReason string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != forgeDeployJobStatusRunning {
		return
	}
	j.status = status
	j.statusDetail = detail
	j.exitCode = exitCode
	j.report = report
	j.supported = supported
	j.unsupportedReason = unsupportedReason
	j.finishedAt = time.Now()
}

// running reports whether this job is still in flight.
func (j *forgeDeployJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == forgeDeployJobStatusRunning
}

// --- the registry ------------------------------------------------------------

// forgeDeployRegistry holds every deploy this daemon process has started.
//
// In-memory on purpose. A handle that survived a daemon restart would be a
// handle to a process that did not, and answering a poll for it with anything
// other than "unknown" would be a guess about a live cluster. A lost handle
// resolves to forgeDeployJobStatusUnknown, which is the honest answer.
type forgeDeployRegistry struct {
	mu   sync.Mutex
	jobs map[string]*forgeDeployJob
}

var deployRegistry = &forgeDeployRegistry{jobs: map[string]*forgeDeployJob{}}

// add registers a job and prunes expired finished ones.
func (r *forgeDeployRegistry) add(job *forgeDeployJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	r.jobs[job.handle] = job
}

// get returns a job by handle, or nil.
func (r *forgeDeployRegistry) get(handle string) *forgeDeployJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[handle]
}

// runningFor returns the handle of a job already deploying this env of this
// project, or "".
//
// Two concurrent applies to one environment race each other's rollouts and
// leave a cluster converging toward two different manifest streams. The
// second one is refused rather than queued: a caller that wanted the first
// one's outcome can poll its handle, and a caller that did not know about it
// needs to be told.
func (r *forgeDeployRegistry) runningFor(projectPath, env string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.projectPath == projectPath && job.env == env && job.running() {
			return job.handle
		}
	}
	return ""
}

// pruneLocked drops finished jobs past their retention. A RUNNING job is never
// pruned however old it is — forgetting an in-flight apply would turn a
// determinate outcome into an unknown one.
func (r *forgeDeployRegistry) pruneLocked() {
	cutoff := time.Now().Add(-forgeDeployJobRetention)
	for handle, job := range r.jobs {
		if job.running() {
			continue
		}
		job.mu.Lock()
		finished := job.finishedAt
		job.mu.Unlock()
		if !finished.IsZero() && finished.Before(cutoff) {
			delete(r.jobs, handle)
		}
	}
}

// --- the deploy runner seam --------------------------------------------------

// runForgeDeploy is the seam for the DETACHED invocation. Tests replace it, and
// that is what lets every test in this package exercise the apply path without
// a cluster.
//
// It is a SECOND seam rather than a reuse of runForge because runForgeSelfExec
// applies forgeInvocationTimeout — the two-minute cap the five read commands
// share. A deploy driven through it would be killed mid-rollout, which is the
// exact failure this whole design exists to avoid, and raising that constant
// would change the budget for five commands that do not want it.
var runForgeDeploy forgeCommandRunner = runForgeDeploySelfExec

// runForgeDeploySelfExec re-executes this binary as `<self> forge <args...>`
// with the project as the child's working directory.
//
// Identical in shape to runForgeSelfExec (see the long comment in cmd_forge.go
// for why forge is a subprocess of ourselves rather than an in-process cobra
// call) with one difference: the ceiling is forgeDeployInvocationTimeout.
func runForgeDeploySelfExec(ctx context.Context, projectDir string, args []string) (forgeCommandResult, error) {
	self, err := os.Executable()
	if err != nil {
		return forgeCommandResult{}, fmt.Errorf("resolve own executable: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, forgeDeployInvocationTimeout)
	defer cancel()

	full := append([]string{"forge", "--silence-experimental"}, args...)
	cmd := exec.CommandContext(ctx, self, full...)
	cmd.Dir = projectDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	result := forgeCommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		result.ExitCode = 0
	case errors.As(runErr, &exitErr):
		result.ExitCode = exitErr.ExitCode()
	default:
		return result, runErr
	}
	return result, nil
}

// --- request / response shapes ----------------------------------------------

// forgeDeployArgs are the fields every deploy command needs.
//
// There is no field here for --target, --namespace, --rollout, --prune or
// --tag, and that is a decision rather than an omission. Each of them changes
// what a deploy does to a live cluster in a way the operator would have to have
// reviewed, and the confirmation token below authorises a SPECIFIC plan. A knob
// the plan did not describe is a knob whose effect nobody approved. `--rollout
// skip` is the sharpest example: it would make every resource report
// not_waited, which is an honest report of a deploy nobody watched converge.
type forgeDeployArgs struct {
	ProjectPath string `json:"project_path"`
	// Env is forge's positional argument.
	Env string `json:"env"`
}

// validate rejects a request before any forge process starts.
//
// The leading-dash check is load-bearing, not decoration: Env lands in argv, so
// an env of "--no-digest" or "--skip-preflight" would be consumed by forge as a
// FLAG — which is how the two escape hatches this command refuses to expose
// would be reachable after all.
func (a forgeDeployArgs) validate() error {
	if strings.TrimSpace(a.ProjectPath) == "" {
		return fmt.Errorf("project_path is required")
	}
	env := strings.TrimSpace(a.Env)
	if env == "" {
		return fmt.Errorf("env is required")
	}
	if strings.HasPrefix(env, "-") {
		return fmt.Errorf("env must not begin with '-' (it would be read as a flag): %q", a.Env)
	}
	return nil
}

// planArgs is the READ-ONLY preview. --dry-run renders the env, runs the
// declared-context guard and the preflight, and returns BEFORE any kubectl
// apply.
//
// --dry-run rather than --explain: explain prints the guard verdict and exits
// without rendering, so it reports no release, no images and no resources. The
// confirmation token below has to be checked against the bound release, and a
// UI previewing a production deploy wants the resource and pinning picture, so
// the preview that answers both questions is the one used. Both are read-only;
// this is strictly the more informative of the two.
func (a forgeDeployArgs) planArgs() []string {
	return []string{"env", "deploy", strings.TrimSpace(a.Env), "--dry-run", "--json"}
}

// applyArgs is the REAL deploy. The only difference from planArgs is the
// absence of --dry-run.
func (a forgeDeployArgs) applyArgs() []string {
	return []string{"env", "deploy", strings.TrimSpace(a.Env), "--json"}
}

// forgeDeployPlanFacts is the MINIMUM the guard needs from forge's document.
//
// The same narrow exception to the transport rule that promote takes, with the
// same boundary: these are IDENTITY fields — which env, which mode, which
// cluster, which release. Not one verdict is re-derived. The guard's only
// question is "is the state the caller described the state that exists", and
// everything else in the document reaches the caller untouched.
//
// Mode, Verdict and Reason are decoded as plain strings rather than through
// forge's strict enums, which are unexported in forge. That is safe in the
// direction that matters because every comparison below treats an UNRECOGNISED
// value as the unsafe one: a mode that is not "dry_run" makes the guard refuse
// rather than proceed.
type forgeDeployPlanFacts struct {
	Env   string `json:"env"`
	Mode  string `json:"mode"`
	Guard struct {
		DeclaredContext string `json:"declared_context"`
		CurrentContext  string `json:"current_context"`
		Verdict         string `json:"verdict"`
		Reason          string `json:"reason"`
		Fix             string `json:"fix"`
	} `json:"guard"`
	Target struct {
		KubeContext string `json:"kube_context"`
		Namespace   string `json:"namespace"`
	} `json:"target"`
	// Release is the release this env is bound to. Empty means the env has no
	// binding at all and forge will deploy by resolved tag.
	Release string `json:"release"`
}

// forge's own tokens, matched as strings. Only the ones the guard branches on.
const (
	forgeDeployModeDryRun        = "dry_run"
	forgeDeployGuardVerdictAllow = "allow"
)

// Refusal reason tokens. Stable machine strings because the RPC layer branches
// on them; the human sentence is Detail.
const (
	// forgeDeployRefusalReasonStaleDeclaredContext: the cluster the env's KCL
	// declares is not the one the caller saw named. THE HEADLINE GUARD — see
	// handleForgeDeployStart.
	forgeDeployRefusalReasonStaleDeclaredContext = "stale_declared_context"

	// forgeDeployRefusalReasonStaleCurrentRelease: the env is bound to a
	// different release than the caller reviewed.
	forgeDeployRefusalReasonStaleCurrentRelease = "stale_current_release"

	// forgeDeployRefusalReasonGuardRefused: forge itself will not deploy —
	// kubectl is unconfigured, or the declared context is absent from the
	// kubeconfig. Surfaced as data rather than swallowed.
	forgeDeployRefusalReasonGuardRefused = "guard_refused"

	// forgeDeployRefusalReasonAlreadyRunning: a deploy of this env is already
	// in flight. Carries its handle so the caller can poll it.
	forgeDeployRefusalReasonAlreadyRunning = "already_running"
)

// forgeDeployRefusal is why a deploy was NOT started, plus the state that was
// actually found.
//
// The found state is carried because "someone changed it" is only actionable if
// the caller can see what it changed TO. Without it the only recovery is a
// blind retry, which against a production cluster is the accident the guard
// exists to prevent.
type forgeDeployRefusal struct {
	// Reason is the stable token.
	Reason string `json:"reason"`
	// Detail is the one-sentence human phrasing.
	Detail string `json:"detail"`

	// The declared-context claim and what was found.
	ExpectedDeclaredContext string `json:"expected_declared_context,omitempty"`
	ActualDeclaredContext   string `json:"actual_declared_context,omitempty"`

	// The bound-release claim and what was found. ActualBound is separate
	// from a non-empty release so "never promoted" stays distinguishable
	// from a binding carrying a blank version.
	ExpectedCurrentRelease string `json:"expected_current_release,omitempty"`
	ExpectedUnbound        bool   `json:"expected_unbound,omitempty"`
	ActualCurrentRelease   string `json:"actual_current_release,omitempty"`
	ActualBound            bool   `json:"actual_bound"`

	// forge's own guard decision, when IT is the refusal.
	GuardVerdict string `json:"guard_verdict,omitempty"`
	GuardReason  string `json:"guard_reason,omitempty"`
	GuardFix     string `json:"guard_fix,omitempty"`

	// RunningHandle is the in-flight deploy's handle, on an
	// already_running refusal.
	RunningHandle string `json:"running_handle,omitempty"`
}

// forgeDeployStartResponse is forge.deploy_start's reply.
//
// A refusal is a SUCCESSFUL daemon response carrying DeployRefused, not a
// transport error, for the same reason promote's is: the refusal is structured
// data and daemon errors cross as flat strings that would lose all of it.
// Turning it into a failed RPC is the job of the layer that faces the browser,
// where an unchecked error fails closed.
type forgeDeployStartResponse struct {
	forgeResponseMeta

	// Handle identifies the detached job. Empty on a refusal — there is no
	// job.
	Handle string `json:"handle,omitempty"`
	Env    string `json:"env,omitempty"`

	// JobStatus is forgeDeployJobStatusRunning on a successful start. It is
	// stated rather than implied so a caller reads the same field here and
	// from deploy_status.
	JobStatus string `json:"job_status,omitempty"`
	StartedAt string `json:"started_at,omitempty"`

	// Report is the GUARD PLAN — the read-only document the guard just
	// computed and authorised the deploy against. It is not an apply
	// report; no apply report exists yet. Carrying it means the caller can
	// render exactly what it approved rather than trusting a preview it
	// fetched earlier, and on a refusal it is the fresh plan against real
	// current state.
	Report json.RawMessage `json:"report,omitempty"`

	// DeployRefused is set exactly when NOTHING WAS STARTED. Absent on a
	// successful start.
	DeployRefused *forgeDeployRefusal `json:"deploy_refused,omitempty"`
}

// forgeDeployStatusResponse is forge.deploy_status's reply: the job's own
// lifecycle, plus forge's report once there is one.
type forgeDeployStatusResponse struct {
	forgeResponseMeta

	Handle string `json:"handle,omitempty"`
	Env    string `json:"env,omitempty"`

	// JobStatus is running / completed / failed / unknown. Read the note on
	// the constants: completed does NOT mean the deploy succeeded.
	JobStatus string `json:"job_status"`
	// JobStatusDetail explains a non-completed terminal state.
	JobStatusDetail string `json:"job_status_detail,omitempty"`

	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`

	// Report is forge's `env deploy --json` document, verbatim, once the job
	// has produced one. Its rollout results carry ready / failed /
	// timed_out / not_waited and nothing on this path reinterprets them.
	Report json.RawMessage `json:"report,omitempty"`
}

// --- forge.deploy_plan -------------------------------------------------------

type forgeDeployPlanRequest struct {
	forgeDeployArgs
}

// handleForgeDeployPlan previews a deploy and applies NOTHING.
//
// `forge env deploy --dry-run --json` renders the env, evaluates the
// declared-context guard, runs the preflight against the live target and
// returns before the first kubectl apply. Read-only, so it needs no
// confirmation token — there is nothing to confirm. This is what a UI calls to
// show a human which cluster, which namespace, which images and which resources
// before asking them to approve it.
//
// It runs synchronously and fits inside the shared two-minute cap because
// nothing here waits for a rollout.
func handleForgeDeployPlan(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeDeployPlanRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.planArgs(),
	})
}

// --- forge.deploy_start ------------------------------------------------------

type forgeDeployStartRequest struct {
	forgeDeployArgs

	// ExpectedDeclaredContext is THE CLUSTER THE CALLER SAW NAMED in the
	// plan, and it is the most important field in this request.
	//
	// forge deploys to the context the env's KCL DECLARES and never to the
	// ambient current-context, so the declared context is the only thing
	// that determines where bytes land — and it can change under a caller
	// between the preview and the confirmation, because it comes from a file
	// anyone can edit. Without this check a UI could show someone
	// "k3d-control-plane", have the KCL edited, and apply to
	// gke_...us-central1_prod on their click. Required, with no
	// "I did not populate it" spelling, because there is no legitimate
	// deploy whose target cluster the operator did not see.
	ExpectedDeclaredContext string `json:"expected_declared_context"`

	// ExpectedCurrentRelease is the release the caller last saw this env
	// bound to. The deploy ships THAT release's pinned digests, so a deploy
	// authorised against one release and executed against another ships code
	// nobody reviewed.
	ExpectedCurrentRelease string `json:"expected_current_release,omitempty"`

	// ExpectUnbound asserts the caller saw NO binding — an env that deploys
	// by resolved tag. An explicit field rather than an empty
	// ExpectedCurrentRelease, because those are three different claims and
	// only two are legitimate: "I saw v1.3.0", "I saw no binding", and "I
	// did not fill this in". Collapsing the last two would let an
	// unpopulated payload authorise shipping whatever the env happens to be
	// bound to now.
	ExpectUnbound bool `json:"expect_unbound,omitempty"`
}

// validateConfirmation checks the caller stated an unambiguous position on both
// halves of the token.
func (r forgeDeployStartRequest) validateConfirmation() error {
	if strings.TrimSpace(r.ExpectedDeclaredContext) == "" {
		return fmt.Errorf("expected_declared_context is required: a deploy must state the cluster the " +
			"operator saw named, because forge applies to the context the env DECLARES and that " +
			"declaration can change between a preview and a confirmation")
	}
	stated := strings.TrimSpace(r.ExpectedCurrentRelease)
	switch {
	case r.ExpectUnbound && stated != "":
		return fmt.Errorf("expect_unbound and expected_current_release %q are contradictory: "+
			"assert either that the env had no binding or which release it was bound to", stated)
	case !r.ExpectUnbound && stated == "":
		return fmt.Errorf("expected_current_release is required (or set expect_unbound for an env with " +
			"no binding): a deploy ships the bound release's pinned digests, so it must state the " +
			"release it expects to ship")
	}
	return nil
}

// handleForgeDeployStart re-checks the caller's view of state and, only if it
// still holds, DETACHES the real deploy and returns a handle.
//
// Sequence, and the ordering is the safety property:
//
//  1. validate the request and the confirmation — no forge process at all for a
//     request that cannot be authorised;
//  2. refuse if a deploy of this env is already in flight;
//  3. run the READ-ONLY plan. This doubles as the gate for every non-deployable
//     state: a missing project dir, a non-forge project and a forge too old all
//     resolve here, before anything is started;
//  4. compare the plan against the caller's claims — declared context first,
//     then bound release — and surface forge's OWN guard refusal. Each
//     disagreement returns a refusal carrying what was found plus that fresh
//     plan, having started nothing;
//  5. only then start the detached apply, and return its handle immediately.
//
// Step 4's declared-context check is the one that does not exist on the promote
// path, and it is the reason this command is not just promote with a different
// argv: promote overwrites a file in the repo, this writes to whichever cluster
// the KCL currently names.
func handleForgeDeployStart(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeDeployStartRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if err := req.validateConfirmation(); err != nil {
		return nil, err
	}

	env := strings.TrimSpace(req.Env)

	// STEP 2 — one deploy per env at a time.
	if handle := deployRegistry.runningFor(req.ProjectPath, env); handle != "" {
		return json.Marshal(forgeDeployStartResponse{
			forgeResponseMeta: forgeResponseMeta{IsForgeProject: true, Supported: true, ForgeVersion: version.Forge()},
			DeployRefused: &forgeDeployRefusal{
				Reason:        forgeDeployRefusalReasonAlreadyRunning,
				RunningHandle: handle,
				Detail: fmt.Sprintf("a deploy of %q is already in flight (handle %s); "+
					"two concurrent applies would race each other's rollout — poll that handle instead",
					env, handle),
			},
		})
	}

	// STEP 3 — the guard plan. Read-only.
	planRaw, err := invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.planArgs(),
	})
	if err != nil {
		return nil, err
	}

	var plan forgeReportResponse
	if err := json.Unmarshal(planRaw, &plan); err != nil {
		return nil, fmt.Errorf("%s: could not read deploy plan: %v", forgeCommandFailedPrefix, err)
	}

	// Not a forge project, or a forge too old for `env deploy --json`. Both
	// are normal structured answers, and in both cases NOTHING WAS STARTED.
	// Returning the plan's own envelope preserves that.
	if !plan.IsForgeProject || !plan.Supported {
		return json.Marshal(forgeDeployStartResponse{
			forgeResponseMeta: plan.forgeResponseMeta,
			Report:            plan.Report,
		})
	}

	var facts forgeDeployPlanFacts
	if err := json.Unmarshal(plan.Report, &facts); err != nil {
		return nil, fmt.Errorf("%s: deploy plan was not the expected shape: %v", forgeCommandFailedPrefix, err)
	}

	// A preview whose mode is not dry_run means --dry-run did not suppress
	// the apply, so the guard just read state that may ALREADY have been
	// mutated. Refuse rather than apply again on top of it. Note the
	// direction: an unrecognised mode lands here too, which is the safe
	// reading.
	if facts.Mode != forgeDeployModeDryRun {
		return nil, fmt.Errorf("%s: deploy preview reported mode %q, not %q — "+
			"the read-only preview appears to have applied; refusing to deploy",
			forgeCommandFailedPrefix, facts.Mode, forgeDeployModeDryRun)
	}

	// Paranoia, cheap: the plan must be about the env that was asked for.
	if got := strings.TrimSpace(facts.Env); got != "" && got != env {
		return nil, fmt.Errorf("%s: deploy plan is for env %q but %q was requested; refusing to deploy",
			forgeCommandFailedPrefix, got, env)
	}

	// STEP 4 — the guards.
	if refusal := forgeDeployStaleState(req, facts); refusal != nil {
		return json.Marshal(forgeDeployStartResponse{
			forgeResponseMeta: plan.forgeResponseMeta,
			// The FRESH plan, so the caller can re-render against the
			// state that actually exists.
			Report:        plan.Report,
			DeployRefused: refusal,
		})
	}

	// STEP 5 — detach.
	job := &forgeDeployJob{
		handle:      uuid.New().String(),
		env:         env,
		projectPath: req.ProjectPath,
		grantID:     daemonpolicy.GrantIDFromContext(ctx),
		startedAt:   time.Now(),
		status:      forgeDeployJobStatusRunning,
		supported:   true,
	}
	deployRegistry.add(job)
	startForgeDeployJob(job, req.applyArgs())

	logging.Info("forge deploy started",
		"handle", job.handle,
		"env", env,
		"declared_context", facts.Guard.DeclaredContext,
		"namespace", facts.Target.Namespace,
		"release", facts.Release)

	return json.Marshal(forgeDeployStartResponse{
		forgeResponseMeta: plan.forgeResponseMeta,
		Handle:            job.handle,
		Env:               env,
		JobStatus:         forgeDeployJobStatusRunning,
		StartedAt:         job.startedAt.UTC().Format(time.RFC3339),
		Report:            plan.Report,
	})
}

// forgeDeployStaleState compares the caller's claims against what the plan
// found, and surfaces forge's own guard refusal.
//
// Nil means every claim held and forge is willing to deploy.
//
// ORDER MATTERS. The declared context is checked FIRST because it decides WHERE
// bytes land, and a wrong-cluster deploy is worse than a wrong-release one: a
// wrong release ships reviewed code to the right place, a wrong cluster ships
// anything at all to production.
func forgeDeployStaleState(req forgeDeployStartRequest, facts forgeDeployPlanFacts) *forgeDeployRefusal {
	expectedContext := strings.TrimSpace(req.ExpectedDeclaredContext)
	actualContext := strings.TrimSpace(facts.Guard.DeclaredContext)

	if actualContext != expectedContext {
		return &forgeDeployRefusal{
			Reason:                  forgeDeployRefusalReasonStaleDeclaredContext,
			ExpectedDeclaredContext: expectedContext,
			ActualDeclaredContext:   actualContext,
			GuardVerdict:            strings.TrimSpace(facts.Guard.Verdict),
			GuardReason:             strings.TrimSpace(facts.Guard.Reason),
			Detail: fmt.Sprintf("env %q now declares kubectl context %q, not the %q this deploy was "+
				"authorised against; the env's KCL changed since the plan was read, and a deploy must "+
				"never land on a cluster the operator did not see named",
				strings.TrimSpace(req.Env), actualContext, expectedContext),
		}
	}

	// forge's OWN verdict. Reported as a structured refusal rather than left
	// for the caller to find in the report body, because an unchecked field
	// is how a refusal becomes a spinner that never resolves.
	if strings.TrimSpace(facts.Guard.Verdict) != forgeDeployGuardVerdictAllow {
		return &forgeDeployRefusal{
			Reason:                  forgeDeployRefusalReasonGuardRefused,
			ExpectedDeclaredContext: expectedContext,
			ActualDeclaredContext:   actualContext,
			GuardVerdict:            strings.TrimSpace(facts.Guard.Verdict),
			GuardReason:             strings.TrimSpace(facts.Guard.Reason),
			GuardFix:                strings.TrimSpace(facts.Guard.Fix),
			Detail: fmt.Sprintf("forge will not deploy env %q: guard verdict %q (%s)",
				strings.TrimSpace(req.Env),
				strings.TrimSpace(facts.Guard.Verdict),
				strings.TrimSpace(facts.Guard.Reason)),
		}
	}

	expectedRelease := strings.TrimSpace(req.ExpectedCurrentRelease)
	actualRelease := strings.TrimSpace(facts.Release)
	// The deploy document omits `release` entirely for an env with no
	// binding, so an empty value IS the unbound signal.
	actualBound := actualRelease != ""

	var detail string
	switch {
	case req.ExpectUnbound && actualBound:
		detail = fmt.Sprintf("env %q was expected to have no release binding but is bound to %s; "+
			"someone promoted it since this plan was read", strings.TrimSpace(req.Env), actualRelease)
	case !req.ExpectUnbound && !actualBound:
		detail = fmt.Sprintf("env %q was expected to be bound to %s but has no binding at all",
			strings.TrimSpace(req.Env), expectedRelease)
	case !req.ExpectUnbound && actualRelease != expectedRelease:
		detail = fmt.Sprintf("env %q is bound to %s, not the expected %s; someone promoted it since "+
			"this plan was read, and deploying would ship a release nobody reviewed",
			strings.TrimSpace(req.Env), actualRelease, expectedRelease)
	default:
		return nil
	}

	return &forgeDeployRefusal{
		Reason:                  forgeDeployRefusalReasonStaleCurrentRelease,
		ExpectedDeclaredContext: expectedContext,
		ActualDeclaredContext:   actualContext,
		ExpectedCurrentRelease:  expectedRelease,
		ExpectedUnbound:         req.ExpectUnbound,
		ActualCurrentRelease:    actualRelease,
		ActualBound:             actualBound,
		Detail:                  detail,
	}
}

// startForgeDeployJob runs the apply in a goroutine and records the terminal
// state.
//
// THE CONTEXT IS DELIBERATELY NOT THE REQUEST'S. A command handler's context is
// cancelled when its response is written, and this response is written
// immediately — inheriting it would kill the deploy at the instant the handle
// was returned. context.Background() with the job's own ceiling is what makes
// the job outlive the RPC that started it, which is the entire point.
//
// THE CLASSIFICATION RULE, AND WHY IT LEANS TOWARD UNKNOWN. Once forge has been
// STARTED, manifests may have reached the cluster. So anything short of "forge
// exited and handed us a parseable document" is UNKNOWN, not failed: a killed
// process, an expired ceiling, an exit with unreadable output. Only a failure to
// start at all, or a forge too old to understand the command, is FAILED —
// because in those two cases nothing can possibly have shipped.
//
// A non-zero exit WITH a report is COMPLETED, carrying forge's exit code and
// document. That is the transport rule: a deploy that ran and failed its rollout
// is the ANSWER, and forge's report states it far better than a status token
// could.
func startForgeDeployJob(job *forgeDeployJob, args []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), forgeDeployInvocationTimeout)
		defer cancel()

		res, err := runForgeDeploy(ctx, job.projectPath, args)

		report := bytes.TrimSpace(res.Stdout)
		hasReport := len(report) > 0 && json.Valid(report)

		switch {
		case err != nil && hasReport:
			// forge emitted its document and then the invocation was
			// disturbed. The document is the better evidence, but the
			// outcome is not determinate, so the report is carried
			// under UNKNOWN rather than laundered into completed.
			job.finish(forgeDeployJobStatusUnknown,
				fmt.Sprintf("forge produced a report but the invocation did not complete cleanly: %v%s",
					err, stderrExcerpt(res.Stderr)),
				res.ExitCode, report, true, "")

		case err != nil:
			// Started and died, or never started. These are different —
			// the first may have shipped bytes, the second cannot
			// have — and the daemon cannot reliably tell them apart
			// from an os/exec error. UNKNOWN is the only honest
			// answer, and it is the safe one: it tells the operator to
			// go and look rather than to assume either way.
			job.finish(forgeDeployJobStatusUnknown,
				fmt.Sprintf("forge could not be run to completion; manifests may or may not have "+
					"reached the cluster: %v%s", err, stderrExcerpt(res.Stderr)),
				res.ExitCode, nil, false, "")

		case hasReport:
			job.finish(forgeDeployJobStatusCompleted, "", res.ExitCode, report, true, "")

		default:
			// No parseable document. If forge did not recognise the
			// command or the flag, it never deployed anything and this
			// is a clean FAILED. Otherwise something stopped it after
			// it started, which is UNKNOWN.
			if reason, unsupported := forgeUnsupportedReason(res); unsupported {
				job.finish(forgeDeployJobStatusFailed,
					"this forge does not support `env deploy --json`; nothing was applied",
					res.ExitCode, nil, false, reason)
				break
			}
			job.finish(forgeDeployJobStatusUnknown,
				fmt.Sprintf("forge exited %d without a parseable report; manifests may or may not "+
					"have reached the cluster%s", res.ExitCode, stderrExcerpt(res.Stderr)),
				res.ExitCode, nil, false, "")
		}

		job.mu.Lock()
		status, detail := job.status, job.statusDetail
		job.mu.Unlock()
		logging.Info("forge deploy finished",
			"handle", job.handle, "env", job.env, "job_status", status, "detail", detail)
	}()
}

// --- forge.deploy_status -----------------------------------------------------

type forgeDeployStatusRequest struct {
	Handle string `json:"handle"`
}

// handleForgeDeployStatus polls a deploy by handle.
//
// AN UNRECOGNISED HANDLE IS NOT AN ERROR AND NOT A FAILURE — it is
// forgeDeployJobStatusUnknown. The registry is in-memory, so a daemon that
// restarted mid-apply has lost the handle for a deploy that may well have
// landed. Answering that poll with a transport error would make the UI show
// "something broke" for a cluster that is converging, and answering it with
// "failed" would invite a retry. Unknown, with a detail that names both
// possibilities, is the only answer that does not assert something the daemon
// cannot know. A genuinely bogus handle lands in the same bucket, which is the
// acceptable cost of never mislabelling the real case.
func handleForgeDeployStatus(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeDeployStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	handle := strings.TrimSpace(req.Handle)
	if handle == "" {
		return nil, fmt.Errorf("handle is required")
	}

	job := deployRegistry.get(handle)
	if job == nil {
		return json.Marshal(forgeDeployStatusResponse{
			forgeResponseMeta: forgeResponseMeta{ForgeVersion: version.Forge()},
			Handle:            handle,
			JobStatus:         forgeDeployJobStatusUnknown,
			JobStatusDetail: "no deploy with this handle is known to the daemon: either it was never " +
				"started, or the daemon restarted while it was in flight and the outcome was lost. " +
				"If a deploy was running, manifests may or may not have reached the cluster — " +
				"verify the environment rather than retrying.",
		})
	}

	// Ownership, same stance as exec.bg_output: a deploy report names
	// clusters, namespaces, image digests and preflight findings, so a
	// connector must not read a first-party deploy's. Indistinguishable from
	// "not found" on purpose.
	if grantID := daemonpolicy.GrantIDFromContext(ctx); grantID != "" && job.grantID != grantID {
		return nil, fmt.Errorf("deploy %s not found", handle)
	}

	return json.Marshal(job.snapshot())
}
