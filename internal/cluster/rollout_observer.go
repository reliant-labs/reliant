package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// Rollout OBSERVATION — reporting per-resource readiness to a caller.
//
// WHY THIS FILE EXISTS. Apply already grades a rollout correctly, but it
// reports the grade as ONE aggregate error ("rollout failed: 3 resources did
// not become ready"). That is the right thing to put in front of a human and
// the wrong thing to hand a program: from outside Apply there is no way to tell
// WHICH resource is which, and — more importantly — no way to tell a resource
// that FAILED from one whose readiness budget simply EXPIRED.
//
// Those are not the same claim. `kubectl rollout status --timeout=90s` exiting
// non-zero because the condition never arrived says nothing about whether the
// Deployment is crash-looping or still pulling a cold image on a new node. A
// consumer told "failed" will cry wolf at a slow deploy; a consumer told
// "ready" would repeat the exact defect RolloutPolicy was written to remove —
// a green deploy over a broken environment. So the third state is not a nicety,
// and it has to be observed HERE, where the wait actually happens, because the
// evidence (kubectl's own timeout message) exists nowhere else.
//
// ADDITIVE BY CONSTRUCTION. Nothing below changes what Apply does, what it
// prints, or what it returns. OnRollout is an optional callback; when it is nil
// every path behaves exactly as before.

// RolloutState is one resource's readiness outcome.
//
// Zero value is RolloutStateUnknown, deliberately: an unreported resource must
// never read as ready. See the CLI's deployJSONRolloutState, which mirrors
// these and whose decoder refuses to default for the same reason.
type RolloutState int

const (
	// RolloutStateUnknown means no outcome was observed.
	RolloutStateUnknown RolloutState = iota
	// RolloutStateReady means the Deployment reached its ready condition, or
	// the one-shot Job completed.
	RolloutStateReady
	// RolloutStateFailed is a genuine, positively-observed failure — a Job
	// that satisfied condition=failed, or a wait that errored for a reason
	// other than expiring its budget.
	RolloutStateFailed
	// RolloutStateTimedOut means the readiness budget expired with no verdict.
	// NOT a success and NOT a failure — the absence of an answer.
	RolloutStateTimedOut
	// RolloutStateNotWaited means forge never asked (rollout mode skip).
	RolloutStateNotWaited
)

// String renders the lowercase wire form.
func (s RolloutState) String() string {
	switch s {
	case RolloutStateReady:
		return "ready"
	case RolloutStateFailed:
		return "failed"
	case RolloutStateTimedOut:
		return "timed_out"
	case RolloutStateNotWaited:
		return "not_waited"
	default:
		return "unknown"
	}
}

// RolloutObservation is one resource's outcome, as handed to an OnRollout
// callback.
type RolloutObservation struct {
	// Kind is the workload kind that was awaited: "Deployment" or "Job".
	Kind string
	// Name is the resource's name.
	Name string
	// State is the three-way outcome.
	State RolloutState
	// Err is the underlying wait error for a failed or timed-out resource,
	// nil when ready. Carried so a caller can report the cause without
	// re-deriving it from the state.
	Err error
}

// classifyWaitError grades a readiness wait error as a timeout or a real
// failure.
//
// The evidence is kubectl's own message. `kubectl rollout status --timeout=`
// and `kubectl wait --timeout=` both exit non-zero with "timed out waiting for
// the condition" when the budget expires, which is the only signal that
// distinguishes "we stopped looking" from "we saw it break". A cancelled or
// deadline-exceeded context is the same class of non-answer: forge stopped
// waiting, so nothing was established either way.
//
// Anything else is a positively-observed failure. Defaulting the OTHER way —
// unrecognised error to timed_out — would be the safer-looking choice and is
// wrong: it would quietly downgrade real, diagnosable failures (a Job that
// reported condition=failed, an unreachable API server) into "unknown", and the
// caller would stop treating them as failures. An error forge cannot attribute
// to a timeout IS a failure; it is graded as one, and the text is carried in
// Err so a reader can see what it actually was.
func classifyWaitError(err error, output string) RolloutState {
	if err == nil {
		return RolloutStateReady
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return RolloutStateTimedOut
	}
	if strings.Contains(strings.ToLower(output), "timed out waiting for the condition") {
		return RolloutStateTimedOut
	}
	return RolloutStateFailed
}

// observeRollout hands one observation to the caller's callback, if there is
// one. Centralised so every wait site reports through the identical path and a
// nil callback is checked in exactly one place.
func observeRollout(opts ApplyOpts, kind, name string, state RolloutState, err error) {
	if opts.OnRollout == nil {
		return
	}
	opts.OnRollout(RolloutObservation{Kind: kind, Name: name, State: state, Err: err})
}

// WaitRolloutObserved is WaitRolloutTimeout that also reports WHICH of the
// three outcomes occurred.
//
// It exists alongside WaitRolloutTimeout rather than replacing it because the
// error is what fails the deploy and must stay byte-identical; the state is
// additional information about that same error. Both are returned from one
// wait, so they cannot describe different attempts.
//
// kubectl's stderr is TEE'd — forwarded to the real stderr exactly as before
// AND captured, so the classification reads kubectl's own words without
// changing anything the user sees.
func WaitRolloutObserved(ctx context.Context, kctx, name, namespace string, timeout time.Duration) (RolloutState, error) {
	if timeout <= 0 {
		timeout = DefaultRolloutTimeout
	}
	var captured bytes.Buffer
	cmd := kubectlCmd(ctx, kctx, "rollout", "status",
		"deployment/"+name,
		"-n", namespace,
		"--timeout="+timeout.String(),
	)
	cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	if err := cmd.Run(); err != nil {
		diagnoseFailedRollout(ctx, kctx, name, namespace)
		return classifyWaitError(err, captured.String()), err
	}
	return RolloutStateReady, nil
}

// WaitJobCompleteObserved is WaitJobCompleteTimeout with the outcome graded.
//
// A Job that satisfied condition=failed is a positively-observed FAILURE — the
// migration ran and did not work — which is materially different from a Job
// whose budget expired while it was still running. WaitJobCompleteTimeout
// already distinguishes the two internally by racing both conditions; this
// surfaces that distinction instead of flattening it into one error.
func WaitJobCompleteObserved(ctx context.Context, kctx, name, namespace string, timeout time.Duration) (RolloutState, error) {
	err := WaitJobCompleteTimeout(ctx, kctx, name, namespace, timeout)
	if err == nil {
		return RolloutStateReady, nil
	}
	// The condition=failed watcher returning cleanly produces this exact
	// error, which is the one case that is a real failure rather than an
	// expired budget.
	if strings.Contains(err.Error(), "job "+name+" failed") {
		return RolloutStateFailed, err
	}
	return classifyWaitError(err, err.Error()), err
}
