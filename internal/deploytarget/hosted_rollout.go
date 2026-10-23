package deploytarget

// GetRollout: the control plane's ONE answer to "has this promotion landed"
// (control-plane docs/design/hosted-deploy-primitives.md §3.2, task F3).
//
// WHY A SECOND READ ALONGSIDE GetStatus. GetStatus answers "is this
// environment converged on what it CURRENTLY DECLARES", which is the right
// question for a drift badge and the wrong one for a deploy's completion
// check. The gap is not cosmetic:
//
//   - It is not pinned to a promotion. A promote of v7 landing while this
//     deploy waits on v6 moves the rows to v7, and the wait then "succeeds"
//     on bytes it was never asked about.
//   - A normal rollout reads DIVERGED mid-flight, so neither failing on
//     DIVERGED nor ignoring it is correct.
//   - Observation can claim the new digest before any new pod serves it:
//     under RollingUpdate the old ReplicaSet keeps readyReplicas up while
//     the new one crash-loops.
//
// The phase folds all three into one server-computed verdict, scoped to the
// promotion whose pins this deploy published. So `forge env deploy`,
// `forge env status --wait`, the in-flight promote refusal and the UI read ONE
// definition of done rather than four that drift apart.
//
// DEPLOY'S THRESHOLD IS DELIBERATELY NOT `env wait`'S. A deploy completes on
// SUCCEEDED **or STABILIZING** — "published and serving" — because making
// every deploy sit out the server's stability window would make every deploy
// two minutes slower for no new information. `forge env status --wait` is the verb
// that holds for the window, because a release GATE is asking the stronger
// question.
//
// The wire shapes are declared HERE rather than imported, for the reason
// stated at the top of hosted.go: forge does not vendor the control plane's
// protos, so what forge expects is written down in explicit local structs.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// procGetRollout is controlplane.v1.DeployService/GetRollout.
const procGetRollout = "controlplane.v1.DeployService/GetRollout"

// rolloutReadAttempts is how many times a deploy's wait retries a FAILING
// rollout read before falling back to the GetStatus poll for the rest of the
// wait.
//
// Three, because the two failure shapes want opposite things. A control
// plane restarting mid-rollout recovers within a tick or two, and giving up
// instantly would weaken the completion check for the whole deploy over a
// blip. But a read that keeps failing must NOT consume the rollout budget:
// without a bound, a deploy whose GetStatus was answering fine the whole time
// would still time out reporting "last status read failed", which blames the
// deploy for a defect in one RPC.
const rolloutReadAttempts = 3

// The DeployRolloutPhase enum's value names, as protojson writes them.
//
// Only the ones this file branches on are named. An unrecognised phase is
// deliberately NOT mapped to anything: it is treated as "not ready", which
// is the conservative reading of a value forge does not understand.
const (
	wireRolloutPhasePrefix      = "DEPLOY_ROLLOUT_PHASE_"
	wireRolloutPhaseStabilizing = "DEPLOY_ROLLOUT_PHASE_STABILIZING"
	wireRolloutPhaseSucceeded   = "DEPLOY_ROLLOUT_PHASE_SUCCEEDED"
	wireRolloutPhaseSuperseded  = "DEPLOY_ROLLOUT_PHASE_SUPERSEDED"
	// WireRolloutPhaseHeld: the promotion was ACCEPTED and RECORDED and is
	// queued on a human action (billing) before the control plane applies
	// it. Exported because the CLI's wait and status read the same value.
	WireRolloutPhaseHeld = "DEPLOY_ROLLOUT_PHASE_HELD"
)

// HostedHold is controlplane.v1.DeployHold: one reason an accepted deploy has
// not started. The control plane applies the promotion — with no further
// command — the moment the hold clears, so a hold is a state to REPORT, never
// a failure to retry.
//
// EVERY FIELD IS RENDERED GENERICALLY. A hold carries its own sentence, remedy
// and link, so a kind this forge has never heard of is still a complete answer;
// Kind is read only for a short label.
type HostedHold struct {
	// Kind is the DeployHoldKind value name (DEPLOY_HOLD_KIND_BILLING).
	Kind        string `json:"kind,omitempty"`
	PromotionID string `json:"promotionId,omitempty"`
	// Reason states the real quantity: "this runs compute (1 workload, 1
	// database) and the organization has no active compute plan".
	Reason string `json:"reason,omitempty"`
	// Fix is the literal next step, written by the control plane.
	Fix string `json:"fix,omitempty"`
	// ActionURL is where a human resolves it: the environment's page in
	// the Reliant web app. Empty when the control plane has none to give.
	ActionURL        string     `json:"actionUrl,omitempty"`
	CallerCanResolve bool       `json:"callerCanResolve,omitempty"`
	HeldSince        *time.Time `json:"heldSince,omitempty"`
}

// Label is the hold's short name: "billing" for DEPLOY_HOLD_KIND_BILLING, and
// a neutral phrase for a kind this build does not know.
func (h HostedHold) Label() string {
	const prefix = "DEPLOY_HOLD_KIND_"
	k := strings.TrimPrefix(h.Kind, prefix)
	if k == h.Kind || k == "" || k == "UNSPECIFIED" {
		return "a human action"
	}
	return strings.ToLower(strings.ReplaceAll(k, "_", " "))
}

// HeldError is a hosted deploy that was ACCEPTED and RECORDED and is QUEUED on
// a human action: nothing failed, nothing needs retrying, and the platform
// applies it on its own once the hold clears. The provider returns it instead
// of waiting out a rollout nobody is moving; the CLI renders it as the queued
// block and its own exit code.
type HeldError struct {
	Env         string
	PromotionID string
	Release     string
	Holds       []HostedHold
}

func (e *HeldError) Error() string {
	label := "a human action"
	if len(e.Holds) > 0 {
		label = e.Holds[0].Label()
	}
	return fmt.Sprintf("hosted env %q: the deploy is recorded and QUEUED, waiting on %s", e.Env, label)
}

// wireRolloutPromotion is the subset of DeployPromotion a deploy's wait
// reads. The CLI's `env wait` decodes the full promotion; a deploy only has
// to say which one it was watching.
type wireRolloutPromotion struct {
	ID             string `json:"id"`
	ReleaseVersion string `json:"releaseVersion,omitempty"`
}

// wireWorkloadRollout is controlplane.v1.DeployWorkloadRollout: one
// workload's progress toward one promotion's frozen pin.
type wireWorkloadRollout struct {
	DeploymentID string `json:"deploymentId"`
	Name         string `json:"name"`
	// Artifact is the release artifact key this workload runs. Empty on
	// an unpinned row (a database, a third-party image).
	Artifact string `json:"artifact,omitempty"`
	// PinnedDigest is from the REQUESTED promotion's resolvedArtifacts,
	// not from whatever the deployment row declares now.
	PinnedDigest   string     `json:"pinnedDigest,omitempty"`
	DesiredDigest  string     `json:"desiredDigest,omitempty"`
	ObservedDigest string     `json:"observedDigest,omitempty"`
	ObservedState  string     `json:"observedState,omitempty"`
	Verdict        string     `json:"verdict,omitempty"`
	Phase          string     `json:"phase,omitempty"`
	StableSince    *time.Time `json:"stableSince,omitempty"`
	ConvergedAt    *time.Time `json:"convergedAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	// UpdatedReplicas against DesiredReplicas is the completion test
	// `kubectl rollout status` applies, and it is what stops a rollout
	// reading healthy before any new pod serves (H1).
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
}

// wireRollout is controlplane.v1.DeployRollout.
type wireRollout struct {
	Promotion wireRolloutPromotion  `json:"promotion"`
	Phase     string                `json:"phase,omitempty"`
	Workloads []wireWorkloadRollout `json:"workloads,omitempty"`
	// Unpinned are the workloads the promotion does not pin. REPORTED,
	// never gating for a release: a database that cannot be
	// release-bound must not be able to fail a release. A DEPLOY still
	// waits on them, because it just published them.
	Unpinned   []wireWorkloadRollout `json:"unpinned,omitempty"`
	StartedAt  *time.Time            `json:"startedAt,omitempty"`
	FinishedAt *time.Time            `json:"finishedAt,omitempty"`
	// A 64-bit proto scalar, so protojson sends it as a STRING — see
	// wireInt64, which accepts that and a bare number.
	StabilityWindowMS   wireInt64 `json:"stabilityWindowMs,omitempty"`
	ConvergesPromotions bool      `json:"convergesPromotions,omitempty"`
	Reason              string    `json:"reason,omitempty"`
	// Holds is set exactly when Phase is HELD: what the queued promotion is
	// waiting on.
	Holds []HostedHold `json:"holds,omitempty"`
}

// errRolloutUnavailable means this control plane cannot answer GetRollout for
// this promotion at all — it does not serve the procedure, or it has no such
// promotion. It is the ONE condition that falls back to the GetStatus poll,
// and it is distinguished from every other failure on purpose: a network
// blip must not silently downgrade the completion check for the rest of the
// deploy.
var errRolloutUnavailable = errors.New("the control plane cannot report a rollout for this promotion")

// codedWireError is the one thing this file needs from a control-plane
// failure: whether it carries a given Connect code. Declared at the
// CONSUMER, so deploytarget keeps its deliberate independence from
// internal/cloud (see the header of hosted.go) and still classifies a
// refusal as data rather than by searching message text.
type codedWireError interface {
	HasCode(code string) bool
}

// The two Connect codes that mean "ask GetStatus instead".
const (
	wireCodeUnimplemented = "unimplemented"
	wireCodeNotFound      = "not_found"
)

// readHostedRollout reads one promotion's rollout. A control plane that does
// not serve the procedure, or does not hold the promotion, returns
// errRolloutUnavailable.
func readHostedRollout(ctx context.Context, c HostedCaller, envID, promotionID string) (wireRollout, error) {
	var resp struct {
		Rollout wireRollout `json:"rollout"`
	}
	req := map[string]any{"environmentId": envID}
	if promotionID != "" {
		req["promotionId"] = promotionID
	}
	if err := c.Call(ctx, procGetRollout, req, &resp); err != nil {
		var coded codedWireError
		if errors.As(err, &coded) &&
			(coded.HasCode(wireCodeUnimplemented) || coded.HasCode(wireCodeNotFound)) {
			return wireRollout{}, fmt.Errorf("%w: %v", errRolloutUnavailable, err)
		}
		return wireRollout{}, err
	}
	return resp.Rollout, nil
}

// rolloutPhaseServing reports whether a pinned workload's phase means "the
// promoted bytes are running and serving".
//
// STABILIZING counts, SUCCEEDED counts, and nothing else does. This is the
// deploy threshold documented at the top of this file: a workload inside the
// stability window IS serving the pin, and a deploy's job is to confirm that
// much. Promoting STABILIZING to "not done" would make every deploy wait out
// the window; demoting SUCCEEDED's window requirement is `env wait`'s
// stronger question, not this one's.
func rolloutPhaseServing(phase string) bool {
	switch phase {
	case wireRolloutPhaseSucceeded, wireRolloutPhaseStabilizing:
		return true
	default:
		return false
	}
}

// rolloutPhaseLabel renders a phase for a human: DEPLOY_ROLLOUT_PHASE_DEGRADED
// → "degraded". An unrecognised value is returned verbatim, never mapped onto
// a phase forge does understand.
func rolloutPhaseLabel(wire string) string {
	if wire == "" {
		return "unknown"
	}
	if trimmed := strings.TrimPrefix(wire, wireRolloutPhasePrefix); trimmed != wire {
		return strings.ToLower(trimmed)
	}
	return wire
}

// describeWorkloadRollout is the one-line reason a workload is not yet
// serving the pin, for the timeout report.
func describeWorkloadRollout(w wireWorkloadRollout) string {
	parts := []string{"phase " + rolloutPhaseLabel(w.Phase)}
	if w.ObservedState != "" {
		parts = append(parts, "observed "+observedName(w.ObservedState))
	}
	// The replica pair is the single most diagnostic thing on a rollout
	// that looks ready and is not: "1 of 3 updated" says the new
	// ReplicaSet has not taken over, which is invisible in the verdict.
	if w.DesiredReplicas > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d updated replicas", w.UpdatedReplicas, w.DesiredReplicas))
	}
	if w.LastError != "" {
		parts = append(parts, "error: "+w.LastError)
	}
	return strings.Join(parts, ", ")
}

// rolloutPendingOf returns the plan items not yet confirmed running against
// this rollout, with a reason for each.
//
// A plan item is matched by NAME against the pinned workloads first and the
// unpinned ones second. An item in neither is pending with "not in the
// rollout yet" — a deployment the control plane has not observed at all must
// never read as ready.
//
// Both halves are waited on, which differs from a release gate on purpose:
// the gate must not let a degraded database fail a healthy release, but a
// DEPLOY just published that database and owes the operator a word on
// whether it came up.
func rolloutPendingOf(plan []hostedPlanItem, rollout wireRollout) (pending []string, reasons map[string]string) {
	pinned := make(map[string]wireWorkloadRollout, len(rollout.Workloads))
	for _, w := range rollout.Workloads {
		pinned[w.Name] = w
	}
	unpinned := make(map[string]wireWorkloadRollout, len(rollout.Unpinned))
	for _, w := range rollout.Unpinned {
		unpinned[w.Name] = w
	}
	reasons = map[string]string{}
	for _, item := range plan {
		switch w, ok := pinned[item.Name]; {
		case ok && rolloutPhaseServing(w.Phase):
		case ok:
			pending = append(pending, item.Name)
			reasons[item.Name] = describeWorkloadRollout(w)
		default:
			u, known := unpinned[item.Name]
			switch {
			case known && (u.ObservedState == wireObservedReady || u.Verdict == wireVerdictConverged):
			case known:
				pending = append(pending, item.Name)
				reasons[item.Name] = describeWorkloadRollout(u)
			default:
				pending = append(pending, item.Name)
				reasons[item.Name] = "not in the rollout yet"
			}
		}
	}
	return pending, reasons
}

// procListConvergences is controlplane.v1.DeployService/ListConvergences.
const procListConvergences = "controlplane.v1.DeployService/ListConvergences"

var errBundleNotApplied = errors.New("the platform has not applied this release yet")

// bundleApplyState is what the control plane says it has applied.
type bundleApplyState struct {
	// known is false when the platform gave no answer at all.
	known   bool
	applied bool
	// current is the sha256 digest the newest observation reports applied.
	current string
	// failure is the reconciler's own words when the newest observation is a
	// FAILED apply of THIS promotion ("reason: message"); empty otherwise.
	// Its revision is the last one applied, i.e. the old one, so applied
	// stays false.
	failure string
}

// pollBundleApplied asks the control plane whether the reconciler has applied
// the bundle this deploy recorded. Readiness is meaningless before that: until
// the hub applies the promoted bundle, workload status describes the previous
// revision. An unimplemented procedure is reported as unknown, so a control
// plane that cannot answer degrades to judging workloads directly rather than
// failing every deploy.
func pollBundleApplied(ctx context.Context, c HostedCaller, envID, promotionID, bundleDigest string) (bundleApplyState, error) {
	var resp struct {
		Convergences []struct {
			PromotionID string `json:"promotionId"`
			Revision    string `json:"revision"`
			State       string `json:"state"`
			Reason      string `json:"reason"`
			Message     string `json:"message"`
		} `json:"convergences"`
	}
	if err := c.Call(ctx, procListConvergences, map[string]any{"environmentId": envID, "limit": 1}, &resp); err != nil {
		var coded codedWireError
		if errors.As(err, &coded) && (coded.HasCode(wireCodeUnimplemented) || coded.HasCode(wireCodeNotFound)) {
			return bundleApplyState{}, err
		}
		// A transient failure keeps waiting rather than judging stale workloads.
		return bundleApplyState{known: true}, err
	}
	if len(resp.Convergences) == 0 {
		return bundleApplyState{known: true}, nil
	}
	row := resp.Convergences[0]
	st := bundleApplyState{known: true, current: revisionDigest(row.Revision)}
	st.applied = bundleDigest != "" && strings.Contains(row.Revision, strings.TrimPrefix(bundleDigest, "sha256:"))
	// "failed" is the control plane's convergence state for a reconciler
	// failure; the row is only about this deploy when it was judged against
	// this promotion.
	if !st.applied && row.State == "failed" && (promotionID == "" || row.PromotionID == promotionID) {
		st.failure = strings.TrimSpace(strings.Join(nonEmpty(row.Reason, row.Message), ": "))
		if st.failure == "" {
			st.failure = "the reconciler reported a failed apply"
		}
	}
	return st, nil
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// revisionDigest extracts the sha256 digest from a reconciler revision such as
// "latest@sha256:abc…"; a revision without one is returned verbatim.
func revisionDigest(rev string) string {
	if i := strings.Index(rev, "sha256:"); i >= 0 {
		return rev[i:]
	}
	return rev
}

func shortDigest(d string) string {
	const shown = len("sha256:") + 12
	if len(d) <= shown {
		return d
	}
	return d[:shown]
}
