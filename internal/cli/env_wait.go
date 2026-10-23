package cli

// `forge env status <env> --wait`: the post-release health gate (control-plane
// docs/design/hosted-deploy-primitives.md §3.2, task F3).
//
// WHAT THIS IS FOR, AND WHY IT IS NOT "POLL GetStatus UNTIL CONVERGED".
// Promote moves a pointer and deploy publishes bytes; neither answers the
// question a pipeline actually gates on — "is the release I just shipped
// serving traffic, and did it stay up?" Polling the environment verdict for
// that lies in five distinct ways, each of which would make the gate worse
// than no gate:
//
//  1. The verdict is not pinned to a promotion. A hotfix promoted while CI
//     waits moves the rows, and the wait then succeeds on bytes it was never
//     asked about.
//  2. A healthy mid-rollout reads DIVERGED, so failing on DIVERGED fails
//     every deploy and ignoring it hides real drift.
//  3. "Ready" can be true before any new pod serves: under RollingUpdate the
//     old ReplicaSet keeps readyReplicas up while the new one crash-loops.
//  4. Nothing distinguishes "finished" from "finished and stayed up" — a pod
//     that crash-loops 40 seconds after Ready passes.
//  5. Unobservable is not healthy, and must not fold into either answer.
//
// So the phase is computed ONCE, server-side, scoped to one promotion's
// frozen pins (GetRollout), and this verb reads it. `forge env deploy`'s
// readiness wait, the in-flight promote refusal and the UI read the same RPC,
// which is what stops four consumers holding four opinions about whether v6
// is done.
//
// THE EXIT CODES ARE THE CONTRACT. A pipeline branches on them directly, so
// the distinctions are load-bearing: 5 (timed out while progressing) says
// retry the WAIT, 6 (superseded) says this release was overtaken, 2 says we
// could not look, and only 1 says the release is wrong. Collapsing any of
// them into 1 fails builds for releases that were fine, which is how a gate
// gets switched off.
//
// DEFAULT IS NOT FAIL-FAST (owner ruling Q4). A pod that crash-loops once on
// a cold start and then settles is ordinary, and exiting on the first
// DEGRADED observation would make the gate flaky in exactly the way that
// destroys trust in it. The rollout fails when the DEADLINE passes while
// degraded, and the report says which observation it was. `--fail-fast` is
// for pipelines that prefer speed to tolerance.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// The §3.2 / Q4 defaults, named rather than written as literals at the flag
// declaration so the help text, the validation and the wait all read one
// value.
const (
	// envWaitDefaultTimeout is the WHOLE budget. 15m covers a cold cloud
	// rollout that pulls fresh images across several workloads plus the
	// server's stability window, and is short enough that a wedged
	// rollout fails the job rather than hanging it to the job's own
	// timeout with no diagnosis.
	envWaitDefaultTimeout = 15 * time.Minute

	// envWaitDefaultInterval is the poll cadence. The phase is derived
	// server-side from committed columns with no cluster round-trip, so
	// 5s is cheap; it is the resolution of the `transitions` list.
	envWaitDefaultInterval = 5 * time.Second
)

// procGetRollout is controlplane.v1.DeployService/GetRollout.
const procGetRollout = "controlplane.v1.DeployService/GetRollout"

// runEnvWaitForCmd is what the command runs. A var so a test can assert the
// OPTIONS the flag layer resolved — specifically that an explicit
// `--timeout 0` became a single read while an unset one kept the budget,
// which is a decision made from cobra's Changed and is therefore only
// observable here. Production is runEnvWait, unchanged.
var runEnvWaitForCmd = runEnvWait

// envWaitOptions is the verb's flags plus the two seams a test states.
type envWaitOptions struct {
	// PromotionID pins the wait to one promotion. Empty = the env's
	// current one.
	PromotionID string
	// Release requires the promotion being waited on to bind this
	// version. Exclusive with PromotionID.
	Release string
	// Timeout is the whole budget. Zero means envWaitDefaultTimeout — see
	// Once for the explicit `--timeout 0` spelling.
	Timeout time.Duration
	// Once reads the phase exactly ONCE and returns, never blocking. It
	// is what `forge env status <env> --wait --timeout 0` means: report
	// where this promotion has got to, right now. It is the snapshot the
	// retired single-read rollout verb was, and absorbing it is why it
	// needed no code of its own.
	//
	// A separate field rather than Timeout == 0, because Timeout's zero
	// value already means "unset, use the default" — the two are
	// indistinguishable in the struct, and only the command layer (which
	// has cobra's Changed) can tell them apart.
	//
	// A still-progressing single read is exitTimedOut (5), through the
	// same table a blocking wait uses: the rollout has not finished, and
	// 5 is precisely "it was progressing when we stopped looking".
	Once bool
	// StableFor is an extra hold after SUCCEEDED, on top of the server's
	// stability window.
	StableFor time.Duration
	// FailFast exits on the first degraded observation.
	FailFast bool
	// IncludeUnpinned lets a degraded unpinned workload fail the gate.
	IncludeUnpinned bool
	// Interval is the poll cadence; zero means envWaitDefaultInterval.
	Interval time.Duration
	// StopOnHold returns exit 7 (queued) at the first HELD read instead of
	// waiting through it. A deploy that was not given --wait sets it: the
	// promotion is accepted and waits on a PERSON (billing), and a command
	// that keeps polling cannot move it. `env status --wait` leaves it
	// false — waiting until it is live is what the caller asked for.
	StopOnHold bool
	// JSON emits one document at the end; WatchJSON adds a line per
	// phase change while waiting.
	JSON      bool
	WatchJSON bool

	// Target is the seam: ONE function resolving everything this wait
	// needs to reach a control plane. Nil resolves the env's declared one
	// (resolveDeclaredWaitTarget), exactly as the ledger does.
	//
	// One function rather than a client/id/endpoint triple, because the
	// three are resolved TOGETHER or not at all — a client without the
	// env id it was resolved against addresses nothing. Three fields
	// would also let production read id and endpoint that only a test
	// ever writes, which is a shape production cannot produce.
	Target func(ctx context.Context, env string) (waitTarget, error)

	// Identity reads the env's managed identity from the resolved control
	// plane. Nil is GetIdentityStatus through the target's client
	// (readIdentityStatus). A test states the control plane's answers here.
	Identity func(ctx context.Context, target waitTarget) (identityStatusDocument, error)
}

// wirePhaseWaitingOnIdentity is the wait's own phase for "the rollout
// succeeded and the env's managed identity is not ready yet". The control
// plane has no such rollout phase: it is forge's, from two reads.
const wirePhaseWaitingOnIdentity = "waiting_on_identity"

// waitTarget is a resolved control plane: who to call, which environment, and
// where that is, for the report.
type waitTarget struct {
	Client        cloudCaller
	EnvironmentID string
	// Endpoint is display only — the Client already addresses it.
	Endpoint string
}

// ─── The report ──────────────────────────────────────────────────────────────

// envWaitWorkloadJSON is one workload's progress, as --json reports it.
type envWaitWorkloadJSON struct {
	Name string `json:"name"`
	// Artifact is the release artifact key this workload runs. Empty
	// means NOT release-bound, which is what puts a row under `unpinned`.
	Artifact string `json:"artifact,omitempty"`
	Phase    string `json:"phase"`
	// PinnedDigest is the promotion's frozen pin; ObservedDigest is what
	// is confirmed running. They differ for the whole of a rollout.
	PinnedDigest   string     `json:"pinned_digest,omitempty"`
	ObservedDigest string     `json:"observed_digest,omitempty"`
	ObservedState  string     `json:"observed_state,omitempty"`
	Verdict        string     `json:"verdict,omitempty"`
	StableSince    *time.Time `json:"stable_since,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	// UpdatedReplicas / DesiredReplicas is the completion test, and the
	// most diagnostic pair on a rollout that looks ready and is not.
	UpdatedReplicas int32 `json:"updated_replicas"`
	DesiredReplicas int32 `json:"desired_replicas"`
}

// envWaitPromotionJSON names the promotion the wait was scoped to. Carried
// even on a failure, because "which release" is the first thing read off a
// red gate.
type envWaitPromotionJSON struct {
	ID         string     `json:"id"`
	Release    string     `json:"release,omitempty"`
	FromEnv    string     `json:"from_env,omitempty"`
	PromotedAt *time.Time `json:"promoted_at,omitempty"`
}

// envWaitTransition is one observed phase change, with when it was seen.
// The LIST is the point: a rollout that went progressing → degraded →
// succeeded tells a very different story from one that went straight to
// succeeded, and only the sequence can distinguish them.
type envWaitTransition struct {
	At    time.Time `json:"at"`
	Phase string    `json:"phase"`
}

// envWaitReport is the `--json` document. It embeds F0's envelope, so ok /
// exit_code / error are stamped from the SAME error the process exits with
// and cannot disagree with it.
type envWaitReport struct {
	jsonEnvelope
	Env           string `json:"env"`
	EnvironmentID string `json:"environment_id,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`

	Promotion *envWaitPromotionJSON `json:"promotion,omitempty"`
	// Phase is the EFFECTIVE phase the exit code came from — which, under
	// --include-unpinned, may be degraded because of an unpinned row.
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// WaitedMS is how long THIS command waited, which is not the
	// rollout's own duration: a wait started after the rollout finished
	// returns immediately.
	WaitedMS int64 `json:"waited_ms"`

	StabilityWindowMS   int64 `json:"stability_window_ms,omitempty"`
	ConvergesPromotions bool  `json:"converges_promotions"`

	// Workloads are the pinned rows — the ones that gate. Always
	// non-nil, so a consumer sees [] rather than null.
	Workloads []envWaitWorkloadJSON `json:"workloads"`
	// Unpinned are reported and (without --include-unpinned) do not gate.
	Unpinned    []envWaitWorkloadJSON `json:"unpinned,omitempty"`
	Transitions []envWaitTransition   `json:"transitions,omitempty"`
	// Queued is set while the promotion is HELD: accepted and recorded,
	// waiting on a human action, with the action URL. Exit 7 when the
	// wait ends there.
	Queued *deployQueuedJSON `json:"queued,omitempty"`
	// Identity is the env's managed identity at the last read; absent when
	// the env declares none. A declared one that is not ready holds the
	// wait after the rollout succeeds (phase waiting_on_identity).
	Identity *envIdentityReport `json:"identity,omitempty"`
}

// ─── Resolution ──────────────────────────────────────────────────────────────

// runEnvWait resolves the env's control plane and waits.
//
// The resolution failures are exit 2, not 1, and that is the §3.A
// distinction: an unreachable control plane or a refused credential means we
// COULD NOT LOOK. Reporting it as 1 would tell a pipeline the release is bad
// on the day someone's token expires.
func runEnvWait(ctx context.Context, env string, opts envWaitOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	report, err := waitForRollout(ctx, env, opts)
	if opts.JSON {
		report.stamp(err)
		if emitErr := emitJSONDocument(report); emitErr != nil && err == nil {
			return emitErr
		}
		return err
	}
	renderEnvWaitText(progressWriter(false), report)
	return err
}

// resolveDeclaredWaitTarget is the production resolution: the control plane
// the ENV'S OWN KCL declares, addressed by the env's control-plane id.
//
// Declarative, like every other hosted verb: the env name is the only input,
// and nothing a previous command left behind can change which control plane
// answers.
func resolveDeclaredWaitTarget(ctx context.Context, env string) (waitTarget, error) {
	decl, err := controlPlaneDeclaration(ctx, env)
	if err != nil {
		return waitTarget{}, undeterminedf("env %q: %v", env, err)
	}
	if decl == nil {
		// A self-managed env has no server-side rollout to read: its
		// ledger is this project's files and nothing observes it. 2
		// rather than 1 — there is nothing wrong with the env, there
		// is just nothing here that can answer the question.
		return waitTarget{}, undeterminedf(
			"env %q declares no hosted control plane, so there is no server-computed rollout to wait on.\n"+
				"  Prove it arrived instead with: forge env status %s", env, env)
	}
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return waitTarget{}, undeterminedf("%v", err)
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return waitTarget{}, undeterminedf("env %q keeps its promotions on the control plane at %s: %v", env, ep.URL, err)
	}
	client := cloud.NewClient(ep, cred)
	envID, err := deploytarget.LookupHostedEnvironment(ctx, client, hostedProjectName(), env)
	if err != nil {
		if errors.Is(err, errHostedEnvNotFound) {
			// Never promoted, never deployed: a conflict with what
			// the caller asked for, not an unobservable env.
			return waitTarget{Endpoint: ep.URL}, &exitCodeError{code: exitConflict, msg: fmt.Sprintf(
				"the control plane at %s has no environment %q, so nothing has been promoted to it", ep.URL, env)}
		}
		return waitTarget{Endpoint: ep.URL}, undeterminedf("resolve env %q on %s: %v", env, ep.URL, err)
	}
	return waitTarget{Client: client, EnvironmentID: envID, Endpoint: ep.URL}, nil
}

// undeterminedf is exit 2: we could not look. Its own constructor because
// every resolution failure in this file is the same category, and spelling
// the code at each site is how one of them ends up as a bare error (exit 1,
// "the release is bad") by accident.
func undeterminedf(format string, args ...any) error {
	return &exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(format, args...)}
}

// ─── The wait ────────────────────────────────────────────────────────────────

// waitForRollout is the poll loop. It returns the report it assembled
// WHATEVER the outcome, so the caller can render a failure as fully as a
// success — a red gate's document is the one most worth reading.
func waitForRollout(ctx context.Context, env string, opts envWaitOptions) (envWaitReport, error) { //nolint:funlen,gocognit // one loop, one phase table: the §3.2 outcome rules read as a sequence and splitting them hides which phase maps to which exit code
	report := envWaitReport{Env: env, Workloads: []envWaitWorkloadJSON{}}
	if opts.Interval <= 0 {
		opts.Interval = envWaitDefaultInterval
	}
	if opts.Timeout <= 0 && !opts.Once {
		opts.Timeout = envWaitDefaultTimeout
	}
	// A single read has no stability hold to observe: --stable-for is a
	// claim about a span of time, and there is no span. Refused rather
	// than ignored, because silently dropping it would report a rollout
	// as stable on the strength of one observation.
	if opts.Once && opts.StableFor > 0 {
		return report, &exitCodeError{code: exitWrong,
			msg: "--stable-for cannot be used with --timeout 0: a single read observes no span of time, so there is nothing to hold stable for"}
	}
	if opts.Release != "" && opts.PromotionID != "" {
		return report, &exitCodeError{code: exitWrong,
			msg: "--promotion and --release name the promotion two different ways; pass one"}
	}

	resolve := opts.Target
	if resolve == nil {
		resolve = resolveDeclaredWaitTarget
	}
	target, err := resolve(ctx, env)
	report.EnvironmentID, report.Endpoint = target.EnvironmentID, target.Endpoint
	if err != nil {
		report.Phase = "unknown"
		return report, err
	}
	client, envID := target.Client, target.EnvironmentID
	identity := newIdentityWatch(opts, target)

	progress := progressWriter(opts.JSON || opts.WatchJSON)
	start := time.Now()
	deadline := start.Add(opts.Timeout)
	var (
		succeededAt      time.Time
		lastPhase        string
		lastIdentityLine string
	)
	for {
		rollout, rerr := readRollout(ctx, client, envID, opts.PromotionID)
		if rerr != nil {
			// TERMINAL failures are returned at once, with the code
			// readRollout already chose. A control plane that does
			// not serve GetRollout will not start serving it inside
			// the budget, and a promotion id it does not hold will
			// not appear — retrying either spends the whole timeout
			// to reach the answer the first call already gave, which
			// is the opposite of a useful gate.
			var coded *exitCodeError
			if errors.As(rerr, &coded) {
				report.WaitedMS = time.Since(start).Milliseconds()
				if report.Phase == "" {
					report.Phase = "unknown"
				}
				return report, rerr
			}
			// Everything else is TRANSIENT and retried while there
			// is budget: a control plane restarting mid-rollout must
			// not fail a release. Out of budget — or asked for a
			// single read, which has no budget to retry within —
			// it is exit 2: we could not look, which is never
			// folded into success.
			if opts.Once || time.Now().After(deadline) || ctx.Err() != nil {
				report.WaitedMS = time.Since(start).Milliseconds()
				if report.Phase == "" {
					report.Phase = "unknown"
				}
				return report, undeterminedf("wait for %s's rollout: %v", env, rerr)
			}
			fmt.Fprintf(progress, "  rollout read failed, retrying: %v\n", rerr)
			sleepUntil(ctx, opts.Interval)
			continue
		}

		applyRolloutToReport(&report, rollout, opts.IncludeUnpinned)
		phase := effectiveRolloutPhase(rollout, opts.IncludeUnpinned)
		report.Identity = identity.observe(ctx)
		if phase == wireRolloutPhaseSucceeded && identity.gating() {
			// The release is serving; the env's identity is not. The wait
			// is not done until both are.
			phase = wirePhaseWaitingOnIdentity
			report.Phase = wirePhaseWaitingOnIdentity
		}
		if phase != lastPhase {
			at := time.Now().UTC()
			report.Transitions = append(report.Transitions, envWaitTransition{At: at, Phase: rolloutPhaseName(phase)})
			if phase != wirePhaseWaitingOnIdentity {
				fmt.Fprintf(progress, "  %s: %s%s\n", env, rolloutPhaseName(phase), parenthesize(rollout.Reason))
			}
			if opts.WatchJSON {
				emitWatchLine(report, at, phase)
			}
			lastPhase = phase
			// --stable-for measures an unbroken run at SUCCEEDED, so
			// leaving the phase restarts it. A rollout that flaps
			// out of succeeded and back has not been stable.
			succeededAt = time.Time{}
		}
		if phase == wirePhaseWaitingOnIdentity {
			// One line per change of the identity's state, so a slow
			// creation reads as progress rather than as a hang.
			if line := report.Identity.line(); line != lastIdentityLine {
				fmt.Fprintf(progress, "  %s: rollout succeeded; waiting on %s\n", env, line)
				lastIdentityLine = line
			}
		}
		// A refused declaration is TERMINAL: only a new deploy changes it,
		// so waiting can only time out on it.
		if id := report.Identity; identity.gating() && (id.terminal || (opts.FailFast && id.failing())) {
			report.WaitedMS = time.Since(start).Milliseconds()
			return report, identityWaitError(env, report, *id, 0)
		}

		// The release-binding check runs on the FIRST successful read,
		// not before it: the promotion has to be read to know what it
		// binds. Checked once — it cannot change for a given promotion.
		if opts.Release != "" && report.Promotion != nil {
			if cerr := checkWaitRelease(env, opts.Release, *report.Promotion); cerr != nil {
				report.WaitedMS = time.Since(start).Milliseconds()
				return report, cerr
			}
			opts.Release = ""
		}
		// The fast refusal: nothing on this control plane will apply
		// the promotion, so waiting can only ever time out, and a
		// timeout would blame the release for a missing converger.
		if !rollout.ConvergesPromotions {
			report.WaitedMS = time.Since(start).Milliseconds()
			return report, undeterminedf(
				"env %q does not converge promotions on this control plane, so this promotion will not roll out on its own.\n"+
					"  Run `forge env deploy %s <version>`, which applies it and waits.", env, env)
		}

		switch phase {
		case wireRolloutPhaseSucceeded:
			if succeededAt.IsZero() {
				succeededAt = time.Now()
			}
			if time.Since(succeededAt) >= opts.StableFor {
				report.WaitedMS = time.Since(start).Milliseconds()
				return report, nil
			}
		case wireRolloutPhaseSuperseded:
			// TERMINAL. A newer promotion replaced this one, so no
			// further polling can change the answer, and exit 6
			// says the wait's subject is gone rather than wrong.
			report.WaitedMS = time.Since(start).Milliseconds()
			return report, &exitCodeError{code: exitSuperseded, msg: fmt.Sprintf(
				"rollout of %s to %s was SUPERSEDED: %s", waitReleaseLabel(report), env,
				emptyOr(rollout.Reason, "a newer promotion replaced the one being waited on"))}
		case wireRolloutPhaseDegraded:
			if opts.FailFast {
				report.WaitedMS = time.Since(start).Milliseconds()
				return report, rolloutDegradedError(env, report, rollout)
			}
			// Otherwise: tolerated. Keep waiting — a cold-start
			// crash loop that settles is the common case, and the
			// deadline below is what turns a persistent one red.
		case wireRolloutPhaseHeld:
			// QUEUED on a person. A deploy without --wait stops here
			// with the block; `env status --wait` keeps polling —
			// the hold clears when they act, and the phase moves on to
			// pending and the ordinary rollout.
			if opts.StopOnHold {
				report.WaitedMS = time.Since(start).Milliseconds()
				return report, queuedRolloutError(env, rollout, 0)
			}
		}

		// Once stops HERE, after exactly one read, and reports where
		// the rollout has got to. The code comes from the same table a
		// blocking wait uses, so `env rollout` and `env wait` cannot
		// disagree about what a phase means.
		if opts.Once {
			report.WaitedMS = time.Since(start).Milliseconds()
			if phase == wirePhaseWaitingOnIdentity {
				return report, identityWaitError(env, report, *report.Identity, 0)
			}
			return report, rolloutSingleReadError(env, report, rollout, phase)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			report.WaitedMS = time.Since(start).Milliseconds()
			if phase == wirePhaseWaitingOnIdentity {
				return report, identityWaitError(env, report, *report.Identity, opts.Timeout)
			}
			return report, rolloutDeadlineError(env, report, rollout, phase, opts.Timeout)
		}
		if !sleepUntil(ctx, opts.Interval) {
			// The context was cancelled: loop once more so the
			// deadline branch above produces the report and the
			// code, rather than returning a bare context error
			// with no document.
			continue
		}
	}
}

// sleepUntil waits for d, or returns false if the context ended first.
func sleepUntil(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// readRollout calls GetRollout. An empty promotionID means the env's current
// promotion.
//
// An `unimplemented` answer is reported as exit 2 with the version hint
// rather than retried to the deadline: a control plane that does not serve
// the procedure will not start serving it within the budget, and spending
// fifteen minutes to discover that is the opposite of a useful gate.
func readRollout(ctx context.Context, client cloudCaller, envID, promotionID string) (wireRollout, error) {
	req := map[string]any{"environmentId": envID}
	if promotionID != "" {
		req["promotionId"] = promotionID
	}
	var resp struct {
		Rollout wireRollout `json:"rollout"`
	}
	if err := client.Call(ctx, procGetRollout, req, &resp); err != nil {
		switch {
		case hostedErrorHasCode(err, cloud.CodeUnimplemented):
			return wireRollout{}, undeterminedf(
				"this control plane does not serve GetRollout, so it cannot report a rollout phase.\n"+
					"  `--wait` needs a control plane with the hosted deploy primitives; read the release state with plain `forge env status <env>` meanwhile: %v", err)
		case hostedErrorHasCode(err, cloud.CodeNotFound):
			return wireRollout{}, &exitCodeError{code: exitConflict, msg: fmt.Sprintf(
				"no such promotion on this control plane%s: %v", parenthesize(promotionID), err)}
		}
		return wireRollout{}, err
	}
	return resp.Rollout, nil
}

// checkWaitRelease enforces --release: the promotion being waited on must
// bind that version.
//
// The two outcomes are different facts. An env with no promotion at all is a
// CONFLICT (3) — the caller asked about a release that was never promoted
// here. An env bound to a DIFFERENT release means this one was overtaken, so
// it is SUPERSEDED (6). Neither is a timeout, which is the failure mode this
// check exists to prevent: without it, `--release v6` against an env on v7
// would wait out the whole budget and then report 5.
func checkWaitRelease(env, want string, got envWaitPromotionJSON) error {
	switch {
	case got.ID == "":
		return &exitCodeError{code: exitConflict, msg: fmt.Sprintf(
			"env %s has no promotion, so release %s was never promoted to it", env, want)}
	case got.Release != want:
		return &exitCodeError{code: exitSuperseded, msg: fmt.Sprintf(
			"env %s is promoted to %s (promotion %s), not to the %s this wait was asked about — it was overtaken",
			env, got.Release, got.ID, want)}
	}
	return nil
}

// ─── Phase folding ───────────────────────────────────────────────────────────

// effectiveRolloutPhase is the phase the exit code is taken from.
//
// Normally it is the server's worst-wins phase over the PINNED workloads,
// verbatim. --include-unpinned additionally lets a degraded unpinned row
// (a managed database, a third-party image) degrade the whole wait.
//
// Unpinned rows are excluded by default on purpose (owner ruling Q4): a
// managed database cannot carry a release artifact, so letting one fail a
// release would make every release hostage to something the release did not
// change — and the failure would be indistinguishable from the release
// being bad. They are still REPORTED, so "the release is healthy and the
// database is not" is a thing the document can say.
func effectiveRolloutPhase(rollout wireRollout, includeUnpinned bool) string {
	phase := rollout.Phase
	if phase == "" {
		phase = wireRolloutPhaseUnspecified
	}
	if !includeUnpinned {
		return phase
	}
	for _, u := range rollout.Unpinned {
		if u.Phase == wireRolloutPhaseDegraded || u.ObservedState == "DEPLOY_OBSERVED_STATE_DEGRADED" {
			// DEGRADED is worst-wins over everything except a
			// SUPERSEDED wait, whose subject no longer exists.
			if phase == wireRolloutPhaseSuperseded {
				return phase
			}
			return wireRolloutPhaseDegraded
		}
	}
	return phase
}

// applyRolloutToReport refreshes the report from the latest read. Called on
// every poll so the document always describes the LAST observation, which is
// what a timeout report has to show.
func applyRolloutToReport(report *envWaitReport, rollout wireRollout, includeUnpinned bool) {
	report.Phase = rolloutPhaseName(effectiveRolloutPhase(rollout, includeUnpinned))
	report.Reason = rollout.Reason
	report.StartedAt = rollout.StartedAt
	report.FinishedAt = rollout.FinishedAt
	report.StabilityWindowMS = rollout.StabilityWindowMS.Int64()
	report.ConvergesPromotions = rollout.ConvergesPromotions
	report.Workloads = waitWorkloadsJSON(rollout.Workloads)
	report.Unpinned = waitWorkloadsJSON(rollout.Unpinned)
	report.Queued = nil
	if rollout.Phase == wireRolloutPhaseHeld {
		report.Queued = (&deployQueuedError{
			Env: report.Env, Release: rollout.Promotion.ReleaseVersion,
			PromotionID: rollout.Promotion.ID, Holds: rollout.Holds,
		}).document()
	}
	if p := rollout.Promotion; p.ID != "" {
		entry := &envWaitPromotionJSON{ID: p.ID, Release: p.ReleaseVersion}
		switch {
		case p.FromEnvironmentName != "":
			entry.FromEnv = p.FromEnvironmentName
		case p.FromEnvironmentID != "":
			entry.FromEnv = p.FromEnvironmentID
		}
		if !p.CreatedAt.IsZero() {
			at := p.CreatedAt
			entry.PromotedAt = &at
		}
		report.Promotion = entry
	}
}

func waitWorkloadsJSON(in []wireWorkloadRollout) []envWaitWorkloadJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]envWaitWorkloadJSON, 0, len(in))
	for _, w := range in {
		out = append(out, envWaitWorkloadJSON{
			Name:            w.Name,
			Artifact:        w.Artifact,
			Phase:           rolloutPhaseName(w.Phase),
			PinnedDigest:    w.PinnedDigest,
			ObservedDigest:  w.ObservedDigest,
			ObservedState:   lowerWireEnum(w.ObservedState, "DEPLOY_OBSERVED_STATE_"),
			Verdict:         lowerWireEnum(w.Verdict, "DEPLOY_VERDICT_"),
			StableSince:     w.StableSince,
			LastError:       w.LastError,
			UpdatedReplicas: w.UpdatedReplicas,
			DesiredReplicas: w.DesiredReplicas,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// lowerWireEnum renders a wire enum value in forge's lower-case vocabulary.
// An unrecognised value keeps its wire spelling rather than being mapped
// onto something forge does understand.
func lowerWireEnum(wire, prefix string) string {
	if wire == "" {
		return ""
	}
	if trimmed := strings.TrimPrefix(wire, prefix); trimmed != wire {
		return strings.ToLower(trimmed)
	}
	return wire
}

// ─── Failure messages ────────────────────────────────────────────────────────

// rolloutDegradedError is exit 1: we looked, and a workload is not serving.
// It NAMES the workloads, because "prod is degraded" sends someone to a
// dashboard while "api: CrashLoopBackOff, 0/3 updated replicas" is a next
// step.
func rolloutDegradedError(env string, report envWaitReport, rollout wireRollout) error {
	return &exitCodeError{code: exitWrong, msg: fmt.Sprintf(
		"rollout of %s to %s DEGRADED: %s", waitReleaseLabel(report), env,
		emptyOr(describeUnhealthyWorkloads(rollout), emptyOr(rollout.Reason, "a workload is not serving")))}
}

// rolloutSingleReadError is `--timeout 0`'s outcome: one read, reported as
// it stands.
//
// It is NOT a timeout message, because nothing timed out — the caller asked
// for a snapshot and got one. But the CODE is the same, through the same
// table: a rollout still progressing when we stopped looking is exitTimedOut
// (5), which is exactly "it was progressing and we did not see it finish".
// Giving a single read its own numbering would mean `env rollout` and
// `env wait` reported different codes for the identical state.
func rolloutSingleReadError(env string, report envWaitReport, rollout wireRollout, phase string) error {
	code := exitCodeForRolloutPhase(phase)
	if code == exitOK {
		return nil
	}
	if phase == wireRolloutPhaseHeld {
		return queuedRolloutError(env, rollout, 0)
	}
	detail := emptyOr(describeUnhealthyWorkloads(rollout), emptyOr(rollout.Reason, "no workload status was reported"))
	return &exitCodeError{code: code, msg: fmt.Sprintf(
		"rollout of %s to %s is %s: %s", waitReleaseLabel(report), env, rolloutPhaseName(phase), detail)}
}

// rolloutDeadlineError is the budget running out. The CODE comes from the
// phase, through the one table every verb reads (exitCodeForRolloutPhase), so
// a rollout that was still progressing exits 5 — retry the wait — and one
// that was degraded exits 1.
func rolloutDeadlineError(env string, report envWaitReport, rollout wireRollout, phase string, budget time.Duration) error {
	if phase == wireRolloutPhaseHeld {
		// Still queued when the budget ran out: 7, with the block, so the
		// caller hands the link to a person rather than retrying a wait
		// that only a person can finish.
		return queuedRolloutError(env, rollout, budget)
	}
	code := exitCodeForRolloutPhase(phase)
	detail := emptyOr(describeUnhealthyWorkloads(rollout), emptyOr(rollout.Reason, "no workload status was reported"))
	hint := ""
	if code == exitTimedOut {
		// The distinction a pipeline acts on, stated in the message as
		// well as the code: this release was never judged bad.
		hint = "\n  The rollout was still progressing, not failing. Retry the wait" +
			" (`forge env status " + env + " --wait --promotion " + waitPromotionID(report) + "`); do not re-promote."
	}
	return &exitCodeError{code: code, msg: fmt.Sprintf(
		"rollout of %s to %s was still %s after %s: %s%s",
		waitReleaseLabel(report), env, rolloutPhaseName(phase), budget, detail, hint)}
}

// newIdentityWatch follows the target env's managed identity through the
// wait (env_identity.go).
func newIdentityWatch(opts envWaitOptions, target waitTarget) *identityWatch {
	if opts.Identity != nil {
		return &identityWatch{read: func(ctx context.Context) (identityStatusDocument, error) { return opts.Identity(ctx, target) }}
	}
	return &identityWatch{read: identityReader(target.Client, target.EnvironmentID)}
}

// identityWaitError is a wait that ended with the rollout serving and the
// env's managed identity not ready. A refused or failing identity is exit 1
// (we looked and it is wrong); one still provisioning is exitTimedOut, like a
// rollout still progressing: retry the wait.
func identityWaitError(env string, report envWaitReport, id envIdentityReport, budget time.Duration) error {
	if id.failing() {
		return &exitCodeError{code: exitWrong, msg: fmt.Sprintf(
			"rollout of %s to %s succeeded, but the environment's managed identity is not usable — %s\n"+
				"  Inspect it with: forge cloud identity status %s", waitReleaseLabel(report), env, id.line(), env)}
	}
	after := ""
	if budget > 0 {
		after = " after " + budget.String()
	}
	return &exitCodeError{code: exitTimedOut, msg: fmt.Sprintf(
		"rollout of %s to %s succeeded, and it is still waiting on its managed identity%s — %s\n"+
			"  The identity was still provisioning, not failing. Retry the wait (`forge env status %s --wait --promotion %s`); do not re-promote.",
		waitReleaseLabel(report), env, after, id.line(), env, waitPromotionID(report))}
}

// queuedRolloutError is a wait that ended on a HELD promotion: exit 7, and the
// queued block naming what it waits on and where to act. waited is the
// budget a --wait spent (zero when the wait stopped at the first read).
func queuedRolloutError(env string, rollout wireRollout, waited time.Duration) error {
	return &deployQueuedError{
		Env: env, Release: rollout.Promotion.ReleaseVersion, PromotionID: rollout.Promotion.ID,
		Holds: rollout.Holds, WaitedFor: waited,
	}
}

// describeUnhealthyWorkloads is the per-workload detail on a failure: every
// pinned row not serving the pin, with its replica counts and last error.
func describeUnhealthyWorkloads(rollout wireRollout) string {
	var lines []string
	for _, w := range rollout.Workloads {
		if w.Phase == wireRolloutPhaseSucceeded {
			continue
		}
		parts := []string{rolloutPhaseName(w.Phase)}
		if w.DesiredReplicas > 0 {
			parts = append(parts, fmt.Sprintf("%d/%d updated replicas", w.UpdatedReplicas, w.DesiredReplicas))
		}
		if w.LastError != "" {
			parts = append(parts, w.LastError)
		}
		lines = append(lines, w.Name+": "+strings.Join(parts, ", "))
	}
	sort.Strings(lines)
	return strings.Join(lines, "; ")
}

func waitReleaseLabel(report envWaitReport) string {
	if report.Promotion != nil && report.Promotion.Release != "" {
		return report.Promotion.Release
	}
	return "the current promotion"
}

func waitPromotionID(report envWaitReport) string {
	if report.Promotion != nil {
		return report.Promotion.ID
	}
	return "<id>"
}

func parenthesize(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func emptyOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// ─── Rendering ───────────────────────────────────────────────────────────────

// emitWatchLine writes one NDJSON line per phase change: the phase, when, and
// the workloads as they stood. Deliberately the same shape as the final
// document minus the envelope, so a consumer parses one schema.
func emitWatchLine(report envWaitReport, at time.Time, phase string) {
	line := report
	line.Phase = rolloutPhaseName(phase)
	line.Transitions = []envWaitTransition{{At: at, Phase: rolloutPhaseName(phase)}}
	// Progress lines are NOT the final answer, so they carry no
	// ok/exit_code: a consumer that read one as the verdict would act on
	// an intermediate phase.
	line.jsonEnvelope = jsonEnvelope{}
	// One line, not indented: NDJSON's whole contract is one document per
	// line, and emitJSONDocument's pretty-printing would break it.
	raw, err := json.Marshal(line)
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stdout, string(raw))
}

// renderEnvWaitText is the human report.
func renderEnvWaitText(out io.Writer, report envWaitReport) {
	fmt.Fprintf(out, "\nRollout of %s to %s: %s\n", waitReleaseLabel(report), report.Env, strings.ToUpper(report.Phase))
	if report.Reason != "" {
		fmt.Fprintf(out, "  %s\n", report.Reason)
	}
	if report.Promotion != nil {
		fmt.Fprintf(out, "  promotion %s", report.Promotion.ID)
		if report.Promotion.FromEnv != "" {
			fmt.Fprintf(out, " (from %s)", report.Promotion.FromEnv)
		}
		fmt.Fprintln(out)
	}
	for _, w := range report.Workloads {
		fmt.Fprintf(out, "  %-24s %-12s %d/%d updated", w.Name, w.Phase, w.UpdatedReplicas, w.DesiredReplicas)
		if w.LastError != "" {
			fmt.Fprintf(out, "  %s", w.LastError)
		}
		fmt.Fprintln(out)
	}
	if report.Identity != nil {
		fmt.Fprintf(out, "  %s\n", report.Identity.line())
	}
	// Printed under their own heading, and never mixed in with the pinned
	// rows, because the two have different authority over the verdict.
	if len(report.Unpinned) > 0 {
		fmt.Fprintln(out, "  not pinned by this release (reported, does not gate):")
		for _, w := range report.Unpinned {
			fmt.Fprintf(out, "    %-22s %s\n", w.Name, emptyOr(w.ObservedState, w.Phase))
		}
	}
	fmt.Fprintf(out, "  waited %s\n", time.Duration(report.WaitedMS)*time.Millisecond)
}
