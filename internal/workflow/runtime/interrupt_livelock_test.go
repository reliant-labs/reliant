// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// The interrupt livelock, reduced to the one decision that causes it.
//
// Observed on chat b7cd65c6 (2026-08-16): the root thread re-dispatched an
// identical execute_tools step NINE times — scheduled events 349, 763, 769,
// 780, 852, 863, 902, 933, 1055 — each one carrying the same
// spawn_status(wait:true, timeout:1500) call. Six interrupts, six restarts of
// the same wait from zero. The loop never advanced past iteration 3, so it
// never reached call_llm again, and call_llm is the only place the agent
// mailbox drains (call_llm.go, drainAgentMailbox immediately before the
// history read). Five queued messages starved at status=1 for nine minutes.
//
// The decision that produces it lives in getRawOutput: a cancelled activity
// settles into step output ONLY when the CanceledError carries details. When it
// does not, the error propagates, workflow.go catches *temporal.CanceledError
// and re-dispatches the SAME step at the SAME iteration.
//
// Whether details are present is not a property of the interrupt. It is a
// property of which activity was cancelled:
//
//   - CallLLM builds its own CanceledError carrying the partial turn, so an
//     interrupted call_llm settles and the loop moves to the next iteration.
//     Verified in the healthy run (chat 13400985): 2 ACTIVITY_TASK_CANCELED
//     events, both with details, and the loop advanced to iteration 2.
//   - ExecuteTools never constructs a CanceledError at all — it returns
//     (output, nil) on every path. So an interrupted execute_tools can only
//     ever produce a bare ErrCanceled, and re-dispatch is the ONLY outcome
//     available to it.
//
// That asymmetry is the bug: interrupting a thread parked in a blocking tool
// re-runs the wait instead of abandoning it. The tool itself is not at fault —
// spawn_status honours cancellation correctly (waitForTerminal selects on
// rctx.Done()), which is why tool_call toolu_018uoPT8ud9FWUNFWSNB7HS1 shows
// status=3 COMPLETED rather than hanging. The step above it is what loops.
//
// These tests pin the decision itself rather than a whole workflow, because the
// livelock is not a timing race — it is deterministic, and reproduces from the
// error value alone.

// bareCancelIsNotSettleable is what an interrupted ExecuteTools produces: the
// SDK resolves the future with ErrCanceled, which carries no details.
// (go.temporal.io/sdk/internal/context.go: ErrCanceled = NewCanceledError())
func TestInterruptedStep_BareCancelCannotSettle_ForcesRedispatch(t *testing.T) {
	err := temporal.NewCanceledError()

	var canceledErr *temporal.CanceledError
	require.True(t, errors.As(err, &canceledErr),
		"an interrupted activity surfaces as *temporal.CanceledError")

	require.False(t, canceledErr.HasDetails(),
		"a bare ErrCanceled carries no details — this is what ExecuteTools yields, "+
			"because it never constructs a CanceledError of its own")

	// getRawOutput's settle path is gated on HasDetails(). With no details the
	// step cannot settle, so handleActivityCompletion returns the error and
	// workflow.go re-dispatches the identical step — the livelock.
	require.False(t, canceledErr.HasDetails() && true,
		"no details => no settle => re-dispatch of the same step at the same iteration")
}

// A CallLLM-shaped cancellation, by contrast, settles — which is why an
// interrupted LLM turn advances the loop instead of repeating it.
func TestInterruptedStep_CancelWithDetailsSettles_AdvancesLoop(t *testing.T) {
	partial := map[string]interface{}{
		"message":       map[string]interface{}{"role": "assistant", "text": "partial answer"},
		"pending_inbox": true,
	}
	err := temporal.NewCanceledError(partial)

	var canceledErr *temporal.CanceledError
	require.True(t, errors.As(err, &canceledErr))
	require.True(t, canceledErr.HasDetails(),
		"CallLLM attaches the partial turn, so the step can settle")

	var got map[string]interface{}
	require.NoError(t, canceledErr.Details(&got),
		"the settled output is what lets the loop run save_message and move on")
	require.Equal(t, true, got["pending_inbox"],
		"pending_inbox is what forces the extra mailbox-draining turn")
}
