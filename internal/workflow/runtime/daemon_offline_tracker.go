// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
)

// DaemonOfflineHaltThreshold is the number of consecutive workflow turns
// (main-loop iterations of DynamicWorkflow) where every daemon-targeted
// activity returned the "no daemon connected" condition AND no daemon-targeted
// activity succeeded, after which the workflow halts itself with a terminal
// error.
//
// Why 3:
//   - 1 is too aggressive: a brief disconnect during a single ExecuteTools
//     fan-out (e.g. the user's laptop briefly lost wifi) would kill the
//     workflow even though the daemon comes back milliseconds later.
//   - 2 still risks killing flows during expected daemon restarts (workspace
//     pod restart, daemon binary upgrade).
//   - 3 gives ~3 LLM-call boundaries of headroom (~30s+ depending on model
//     latency) while still bounding the "stuck thinking" window to something
//     the user will tolerate. The threshold is a const, not an env var: it's
//     a quality-of-life knob, not an operator-tunable knob.
//
// If we later observe this is too aggressive or too lax, change the const —
// not introduce a flag.
const DaemonOfflineHaltThreshold = 3

// DaemonOfflineHaltMarker is the stable substring planted in the terminal
// error message when DynamicWorkflow halts due to consecutive daemon-offline
// turns. The frontend chat-error UI scans for this marker (in chatStore.ts)
// and renders a "Reconnect workspace" affordance instead of a generic toast.
//
// The canonical contract lives in internal/chatmarkers — this constant is a
// thin local alias kept for legacy call-site / test readability. New code
// should refer to chatmarkers.KindDaemonOfflineHalt directly.
const DaemonOfflineHaltMarker = string(chatmarkers.KindDaemonOfflineHalt)

// DaemonOfflineTracker accumulates per-turn observations about daemon-targeted
// activity outcomes, then evaluates whether the workflow should halt.
//
// A "turn" is one iteration of the DynamicWorkflow main loop: collect events
// → find triggered steps → execute steps → wait for completions → loop. The
// tracker is observed AT THE END of each iteration; ObserveTurnBoundary
// either bumps or resets the consecutive-offline-turn counter.
//
// Lifetime: one tracker per workflow execution. Lives on the workflow's
// (deterministic) stack and is never persisted — replay reconstructs it from
// the recorded activity outcomes.
//
// Counting policy:
//   - At least one daemon-targeted observation in the turn AND every one
//     of them was daemon-offline → consecutive++.
//   - At least one daemon-targeted observation in the turn AND at least one
//     succeeded → consecutive = 0 (the daemon is back; clear the streak).
//   - Zero daemon-targeted observations in the turn → consecutive UNCHANGED
//     (a CallLLM-only turn is neither evidence for nor against the daemon
//     being offline; it must not reset the streak, otherwise a streak of
//     [ExecuteTools-fail, CallLLM, ExecuteTools-fail, CallLLM, ...] would
//     never trip the halt).
type DaemonOfflineTracker struct {
	// consecutiveOfflineTurns is the streak length. Compared against
	// DaemonOfflineHaltThreshold to decide when to halt.
	consecutiveOfflineTurns int

	// turn-scoped flags, reset by Reset() at the start of each turn.
	turnSawDaemonOffline bool
	turnSawDaemonSuccess bool
}

// NewDaemonOfflineTracker creates an empty tracker.
func NewDaemonOfflineTracker() *DaemonOfflineTracker {
	return &DaemonOfflineTracker{}
}

// Reset clears the per-turn observation flags. Call at the START of each turn
// (i.e. the top of the main-loop iteration).
func (t *DaemonOfflineTracker) Reset() {
	if t == nil {
		return
	}
	t.turnSawDaemonOffline = false
	t.turnSawDaemonSuccess = false
}

// ObserveStepError records the outcome of a step that returned a Go error.
// The error may be inspected for the daemon-offline marker; non-daemon errors
// are ignored (other halt/retry mechanisms handle them — RetryExhausted, etc.).
func (t *DaemonOfflineTracker) ObserveStepError(err error) {
	if t == nil || err == nil {
		return
	}
	if daemonoffline.IsError(err) {
		t.turnSawDaemonOffline = true
	}
}

// ObserveStepOutput records the outcome of a step that succeeded at the
// Temporal-activity layer but whose payload may carry daemon-offline tool
// results. ExecuteTools is the canonical example: when the daemon is offline,
// the activity returns successfully with a tool_results array where each
// element has IsError=true and content carrying the daemon-offline substring.
//
// Behavior:
//   - The step targeted the daemon AND every daemon-targeted tool failed with
//     daemon-offline → turnSawDaemonOffline.
//   - The step targeted the daemon AND at least one daemon-targeted tool
//     succeeded → turnSawDaemonSuccess (the daemon answered for SOMETHING in
//     this turn).
//   - The step didn't target the daemon (e.g. CallLLM, save_message) → no-op.
//
// stepID is taken for log/debug visibility; the helper itself doesn't branch
// on it.
func (t *DaemonOfflineTracker) ObserveStepOutput(stepID string, output map[string]interface{}) {
	if t == nil || output == nil {
		return
	}

	toolResults, ok := output["tool_results"].([]interface{})
	if !ok || len(toolResults) == 0 {
		return
	}

	// We're looking at an ExecuteTools-style output. Scan the tool_results
	// array. Note that not every tool in the array necessarily targeted the
	// daemon — response tools and ask_user run inline. So we use the
	// daemon-offline substring as the SOLE positive signal: a result with the
	// substring counts as a daemon failure; a result WITHOUT the substring
	// and with is_error=false counts as a daemon success ONLY if it COULD
	// have been a daemon call. To stay conservative and avoid resetting the
	// counter on response-tool-only turns, we only mark "saw success" when
	// we ALSO saw a daemon-offline result somewhere — that proves the user
	// is in a daemon-targeting workflow.
	//
	// In practice the common case is "some tools succeed, some fail" once
	// the daemon comes back partially, which is correctly handled: any
	// daemon-offline result counts, and any non-error result in that same
	// turn resets the streak.
	sawOfflineInThisStep := false
	sawSuccessInThisStep := false

	for _, item := range toolResults {
		tr, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		content, _ := tr["content"].(string)
		isError, _ := tr["is_error"].(bool)

		if isError && daemonoffline.IsToolResultContent(content) {
			sawOfflineInThisStep = true
			continue
		}
		// Treat any non-error tool result as evidence of daemon liveness if
		// THIS step also contained an offline result — see comment above.
		if !isError {
			sawSuccessInThisStep = true
		}
	}

	if sawOfflineInThisStep {
		t.turnSawDaemonOffline = true
	}
	if sawOfflineInThisStep && sawSuccessInThisStep {
		// Partial success: the daemon answered at least one call. Reset the
		// streak — the user is back in business even if some calls failed.
		t.turnSawDaemonSuccess = true
	}
	// We deliberately don't mark turnSawDaemonSuccess for a clean
	// no-offline-results turn: those results might all be from inline
	// response tools and ask_user, neither of which prove daemon liveness.
}

// ObserveDaemonSuccess is an explicit "daemon answered" signal for sites that
// know they hit the daemon successfully (e.g. ExecuteRunStep returning no
// error). The workflow loop calls this when a step that targets the daemon
// completes without error.
func (t *DaemonOfflineTracker) ObserveDaemonSuccess(stepID string) {
	if t == nil {
		return
	}
	t.turnSawDaemonSuccess = true
}

// ObserveTurnBoundary finalizes the current turn's observations and updates
// the consecutive-offline-turn counter. Returns the new counter value so the
// caller can compare against the halt threshold.
//
// Call AT THE END of each main-loop iteration, AFTER all step completions
// for that iteration have been observed via ObserveStepError /
// ObserveStepOutput.
func (t *DaemonOfflineTracker) ObserveTurnBoundary() int {
	if t == nil {
		return 0
	}
	switch {
	case t.turnSawDaemonSuccess:
		// Daemon answered for at least one call this turn — back online.
		t.consecutiveOfflineTurns = 0
	case t.turnSawDaemonOffline:
		// Saw offline failures with no offsetting success.
		t.consecutiveOfflineTurns++
	default:
		// No daemon-targeted activity this turn (e.g. CallLLM-only turn).
		// Leave the counter unchanged.
	}
	return t.consecutiveOfflineTurns
}

// ConsecutiveOfflineTurns exposes the streak length without mutating state.
// Intended for tests / observability.
func (t *DaemonOfflineTracker) ConsecutiveOfflineTurns() int {
	if t == nil {
		return 0
	}
	return t.consecutiveOfflineTurns
}

// HaltError returns the terminal error that DynamicWorkflow returns when the
// consecutive-offline-turn streak meets the halt threshold. The message
// carries the chatmarkers.KindDaemonOfflineHalt marker so the frontend can
// render a structured recovery affordance.
func HaltError(consecutiveTurns int) error {
	msg := fmt.Sprintf(
		"daemon offline for %d consecutive turns; halting workflow. Reconnect the workspace and start a new turn.",
		consecutiveTurns,
	)
	return fmt.Errorf("%s", chatmarkers.Wrap(
		chatmarkers.KindDaemonOfflineHalt,
		fmt.Sprintf("%d", consecutiveTurns),
		msg,
	))
}
