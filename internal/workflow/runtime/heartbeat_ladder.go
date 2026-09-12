// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"

	"go.temporal.io/sdk/temporal"
)

// heartbeatCancelErrorType is the ApplicationError type ActivityWrapper stamps
// on a step that was killed by a failed heartbeat RPC rather than by anything
// wrong with the step itself. See spuriousHeartbeatCancel (registry.go).
const heartbeatCancelErrorType = "HeartbeatCancel"

// maxHeartbeatLadderRestarts bounds how many times a single step may be handed
// a FRESH retry ladder because its previous ladder was consumed entirely by
// heartbeat failures.
//
// A bound is required: without one, a Temporal server that is down rather than
// merely busy would have the step re-dispatch forever, which is the livelock
// the auto-pause exists to prevent. With it, a step gets
// stepActivityMaxAttempts*(1+maxHeartbeatLadderRestarts) attempts against pure
// infrastructure failure before the chat pauses and asks the user.
//
// Sized against measured burst duration. The 2026-09-11 bursts ran 3-5 minutes;
// one ladder spans roughly 15s of backoff (1+2+4+8), so three extra ladders
// plus the original covers about a minute of continuous failure. Beyond that,
// something is genuinely wrong and pausing to tell the user is the honest
// outcome rather than spinning silently.
const maxHeartbeatLadderRestarts = 3

// heartbeatCancelExhausted reports whether a retry-exhausted step ran out of
// attempts because of failed heartbeat RPCs rather than because the work itself
// failed.
//
// This distinction is the difference between a chat that rides out a slow
// Temporal server and one that stops dead in front of the user. A heartbeat
// cancel means the activity was HEALTHY — it was streaming tokens — and the
// SDK killed its context because one round trip to the Temporal server missed
// a 1-2s deadline. Spending the step's whole 5-attempt budget on that, then
// auto-pausing, is how chat ee527bdd died: all five attempts landed inside one
// 60-failure burst, none of them for a reason the model or the user could act
// on.
//
// A real failure — a rate limit, a bad request, a provider outage — still
// pauses on the first exhausted ladder, because retrying those harder does not
// make them succeed and the user needs to be told.
func heartbeatCancelExhausted(err error) bool {
	if err == nil {
		return false
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		return false
	}
	return appErr.Type() == heartbeatCancelErrorType
}

// ladderRestarts counts fresh retry ladders granted per step, so the bound in
// maxHeartbeatLadderRestarts is enforced per step rather than per workflow.
//
// Determinism: the zero value is usable and every mutation is driven by
// activity completions the workflow already observes in a fixed replay order,
// so the counter reconstructs identically on replay.
type ladderRestarts map[string]int

// grantRestart reports whether stepID may be re-dispatched with a fresh ladder,
// recording the grant when it returns true.
func (r *ladderRestarts) grantRestart(stepID string) bool {
	if *r == nil {
		*r = make(ladderRestarts)
	}
	if (*r)[stepID] >= maxHeartbeatLadderRestarts {
		return false
	}
	(*r)[stepID]++
	return true
}

// clear forgets a step's restart history once it succeeds, so a long chat that
// hits an unrelated burst hours later gets the full allowance again rather than
// inheriting a spent one.
func (r *ladderRestarts) clear(stepID string) {
	if *r != nil {
		delete(*r, stepID)
	}
}
