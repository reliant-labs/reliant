package deploystate

import (
	"context"
	"fmt"
	"time"
)

// Action is what a reconcile pass concluded it should DO. Separate from
// [State], which is what it concluded is TRUE, because the same truth
// yields different actions under different policy — and conflating them
// is how a reporting tool grows a write path nobody reviewed.
type Action int

const (
	// ActionNone means nothing to do: the target is converged, or it is
	// unknown and there is nothing to act on.
	ActionNone Action = iota
	// ActionReport means drift exists and forge will say so and stop.
	ActionReport
	// ActionConverge means drift exists and policy permits correcting it.
	ActionConverge
	// ActionWait means the target is matching but has not yet held its
	// shape past the stability window. Distinct from None: a caller
	// arming a rollback timer needs to know the clock is still running.
	ActionWait
)

// String renders the action for logs.
func (a Action) String() string {
	switch a {
	case ActionReport:
		return "report"
	case ActionConverge:
		return "converge"
	case ActionWait:
		return "wait"
	default:
		return "none"
	}
}

// Decision is one pass's conclusion about one target.
type Decision struct {
	Key    Key
	State  State
	Policy Policy
	Action Action

	// Drifted lists the OWNED fields that do not match. Empty under
	// every action but report and converge.
	Drifted []Field

	// Reason is one line explaining the action, for the log line a human
	// reads when they ask why forge did or did not do something.
	Reason string
}

// Decide computes the action for one record under one policy.
//
// The policy gate is a SINGLE branch and it comes before any converge
// path, so there is exactly one place in this package where the decision
// to change something is made. A converge action that could be reached
// by two routes is one that will eventually be reached by a route nobody
// checked the policy on.
func Decide(rec Record, policy Policy, now time.Time, window time.Duration) Decision {
	state := rec.Evaluate(now, window)
	d := Decision{Key: rec.Key, State: state, Policy: policy}

	switch state {
	case StateUnknown:
		d.Action = ActionNone
		d.Reason = "target cannot be observed; nothing to act on"
		return d

	case StateConverging:
		d.Action = ActionWait
		d.Reason = "matches declaration but has not held it past the stability window"
		return d

	case StateConverged:
		d.Action = ActionNone
		d.Reason = "converged and stable"
		return d

	case StateDegraded:
		// NOT converge, even under PolicyConverge, and this is the case
		// most worth arguing about. A degraded target is one that is not
		// serving — re-applying the same declaration onto a crash-looping
		// workload produces an identical crash loop, and does it while
		// erasing whatever an engineer had changed to diagnose it. The
		// correct automatic response to "it is broken and it matches what
		// we asked for" is to say so loudly, not to ask again.
		d.Action = ActionReport
		d.Drifted = rec.DriftedFields()
		d.Reason = "target is not serving; reporting rather than re-applying"
		return d

	case StateDiverged:
		d.Drifted = rec.DriftedFields()
		switch {
		case policy == PolicyPinned:
			d.Action = ActionReport
			d.Reason = "drift detected; environment is pinned, so no change will be made"
		case policy.AllowsConverge():
			d.Action = ActionConverge
			d.Reason = fmt.Sprintf("drift in %v; policy permits convergence", d.Drifted)
		default:
			d.Action = ActionReport
			d.Reason = fmt.Sprintf("drift in %v; policy is observe, so no change will be made", d.Drifted)
		}
		return d
	}

	d.Action = ActionNone
	d.Reason = "no verdict"
	return d
}

// DecideEnv reads the environment's policy and decides for every record
// in it.
//
// THE POLICY READ HAPPENS HERE, ON EVERY CALL, and nothing in this
// package caches it. That is the mechanism behind "opting out must be
// instant": a worker calling DecideEnv each pass re-reads the policy each
// pass, so setting an environment to pinned takes effect on the next
// pass — no deploy, no restart, no artifact rebuild. A policy read once
// at construction, or compiled into a deploy artifact, would be correct
// in every test and wrong at the only moment anyone cares about it.
//
// A policy read that FAILS aborts the whole environment rather than
// falling back to a default. Falling back would mean a backend outage
// silently re-enables convergence on an environment an operator pinned,
// which inverts the guarantee the pinned setting exists to give.
func DecideEnv(ctx context.Context, store Store, env string, now time.Time, window time.Duration) ([]Decision, error) {
	policy, err := store.Policy(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("deploystate: reading policy for %q: %w", env, err)
	}
	records, err := store.List(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("deploystate: listing records for %q: %w", env, err)
	}
	out := make([]Decision, 0, len(records))
	for _, rec := range records {
		out = append(out, Decide(rec, policy, now, window))
	}
	return out, nil
}
