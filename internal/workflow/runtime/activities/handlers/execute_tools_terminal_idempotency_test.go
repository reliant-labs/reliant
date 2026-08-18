// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE INTERRUPT LIVELOCK (specs/interrupt-pause-spec.md #2, chat b7cd65c6).
//
// An interrupt re-dispatches a cancelled step into a FRESH, uncancelled
// context (ThreadInterrupt mints a new WithCancel per epoch), so
// executeSingleTool's ctx.Err() short-circuit never fires on re-entry. Before
// this fix, that meant a tool with an already-terminal row ran again from
// scratch -- restarting a blocking wait (spawn_status(wait:true) restarted
// nine times, starving the mailbox for nine minutes).
//
// The fix: before dispatch, check whether toolCallID already reached a
// terminal status. If so, return the recorded result instead of executing.
// These tests pin that a terminal row stops re-execution, and that a
// non-terminal or missing row does NOT -- breaking ordinary retries would be
// just as bad as the livelock itself.

// TestTerminalIdempotency_CompletedRowIsNotReExecuted is the direct
// regression test for the livelock: a tool_call_id with a durable Completed
// row and a matching result must short-circuit on the SECOND dispatch rather
// than running the executor again.
func TestTerminalIdempotency_CompletedRowIsNotReExecuted(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{
		Success: true,
		Content: "sleep finished normally",
	})

	// First dispatch: runs for real, terminal row + result land.
	first := f.executeTool(t, toolCallID, "spawn_status", `{"wait":true}`, executor)
	require.Len(t, first.ToolResults, 1)
	assert.Equal(t, 1, executor.GetExecutionCount(toolCallID))

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	require.True(t, call.Status.IsTerminal(), "fixture must reach a terminal status for this test to mean anything")

	// Second dispatch models the re-entered step after interrupt: same
	// tool_call_id, fresh uncancelled context (mockToolExecutor's ExecuteTool
	// sees no cancellation at all). Before the fix this ran the tool again.
	second := f.executeTool(t, toolCallID, "spawn_status", `{"wait":true}`, executor)
	require.Len(t, second.ToolResults, 1)

	assert.Equal(t, 1, executor.GetExecutionCount(toolCallID),
		"a tool with an already-terminal row must not be re-executed on re-entry")
	assert.Equal(t, first.ToolResults[0].GetContent(), second.ToolResults[0].GetContent(),
		"the re-entered dispatch must return the ORIGINAL recorded result")
	assert.False(t, second.ToolResults[0].GetIsError())
}

// TestTerminalIdempotency_CancelledRowIsNotReExecuted covers the exact shape
// of chat b7cd65c6: a call cancelled while pending (never dispatched) must
// still block re-execution on the next dispatch of the same id.
func TestTerminalIdempotency_CancelledRowIsNotReExecuted(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()

	// Directly write a terminal Cancelled row + result, modeling the state
	// left behind by a prior dispatch that was cancelled while pending
	// (executeSingleTool's own pending-cancel path does exactly this).
	completedAt := time.Now()
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      f.chatID,
		ToolName:    "spawn_status",
		Status:      core.ToolCallStatusCancelled,
		RequestedAt: completedAt,
		CompletedAt: &completedAt,
		CreatedAt:   completedAt,
		UpdatedAt:   completedAt,
	}))
	require.NoError(t, f.h.Repo().UpsertToolCallResult(ctx, &core.ToolCallResult{
		ToolCallID: toolCallID,
		Content:    "Tool execution cancelled by user",
		IsError:    true,
		CreatedAt:  completedAt,
		UpdatedAt:  completedAt,
	}))

	output := f.executeTool(t, toolCallID, "spawn_status", `{"wait":true}`, executor)
	require.Len(t, output.ToolResults, 1)

	assert.Equal(t, 0, executor.GetExecutionCount(toolCallID),
		"a cancelled tool call must never be re-dispatched to the executor")
	assert.True(t, output.ToolResults[0].GetIsError())
	assert.Equal(t, "Tool execution cancelled by user", output.ToolResults[0].GetContent(),
		"the recorded cancellation result must be returned verbatim")
}

// TestTerminalIdempotency_TerminalRowWithNoResultFallsBackToStub covers a
// terminal row whose result content never landed (e.g. a historical row that
// predates durable status, or a lost race). Must still refuse to re-execute,
// falling back to the same InterruptedToolResultContent stub every other
// dangling-tool-call repair path uses.
func TestTerminalIdempotency_TerminalRowWithNoResultFallsBackToStub(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()

	completedAt := time.Now()
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusCancelled,
		RequestedAt: completedAt,
		CompletedAt: &completedAt,
		CreatedAt:   completedAt,
		UpdatedAt:   completedAt,
	}))
	// Deliberately no UpsertToolCallResult.

	output := f.executeTool(t, toolCallID, "bash", `{"command":"echo hi"}`, executor)
	require.Len(t, output.ToolResults, 1)

	assert.Equal(t, 0, executor.GetExecutionCount(toolCallID),
		"a terminal row must block re-execution even with no result content")
	assert.True(t, output.ToolResults[0].GetIsError())
	assert.Equal(t, InterruptedToolResultContent, output.ToolResults[0].GetContent())
}

// TestTerminalIdempotency_NoPriorRowStillExecutes is the control: a
// tool_call_id with NO row at all is the ordinary first-dispatch case and
// must execute normally. Guards against an overly broad check that treats a
// missing row as terminal.
func TestTerminalIdempotency_NoPriorRowStillExecutes(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{Success: true, Content: "fresh run"})

	output := f.executeTool(t, toolCallID, "bash", `{"command":"echo hi"}`, executor)
	require.Len(t, output.ToolResults, 1)

	assert.Equal(t, 1, executor.GetExecutionCount(toolCallID),
		"a tool call with no prior row must execute normally")
	assert.Equal(t, "fresh run", output.ToolResults[0].GetContent())
}

// TestTerminalIdempotency_PendingRowStillExecutes and
// TestTerminalIdempotency_ExecutingRowStillExecutes are the other control:
// non-terminal rows (Pending/Executing) must NOT be treated as settled, or
// every ordinary Temporal activity retry would return a stale "still running"
// answer instead of actually running the tool.
func TestTerminalIdempotency_PendingRowStillExecutes(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{Success: true, Content: "ran after pending"})

	now := time.Now()
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusPending,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	output := f.executeTool(t, toolCallID, "bash", `{"command":"echo hi"}`, executor)
	require.Len(t, output.ToolResults, 1)

	assert.Equal(t, 1, executor.GetExecutionCount(toolCallID),
		"a PENDING row is not terminal and must still execute -- this is what makes an ordinary retry work")
	assert.Equal(t, "ran after pending", output.ToolResults[0].GetContent())
}

func TestTerminalIdempotency_ExecutingRowStillExecutes(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{Success: true, Content: "ran after executing"})

	now := time.Now()
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: now,
		StartedAt:   &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	output := f.executeTool(t, toolCallID, "bash", `{"command":"echo hi"}`, executor)
	require.Len(t, output.ToolResults, 1)

	assert.Equal(t, 1, executor.GetExecutionCount(toolCallID),
		"an EXECUTING row is not terminal and must still execute")
	assert.Equal(t, "ran after executing", output.ToolResults[0].GetContent())
}

// TestTerminalIdempotency_BackgroundedRowStillExecutes: Backgrounded is
// deliberately NOT terminal (core.ToolCallStatus.IsTerminal) -- the process
// is still running and owes a real outcome later. Treating it as settled here
// would abandon a live background process instead of letting it report in.
func TestTerminalIdempotency_BackgroundedRowStillExecutes(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{Success: true, Content: "ran after backgrounded"})

	now := time.Now()
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusBackgrounded,
		RequestedAt: now,
		StartedAt:   &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	output := f.executeTool(t, toolCallID, "bash", `{"command":"npm run dev"}`, executor)
	require.Len(t, output.ToolResults, 1)

	assert.Equal(t, 1, executor.GetExecutionCount(toolCallID),
		"Backgrounded is not terminal and must not block re-dispatch")
	assert.Equal(t, "ran after backgrounded", output.ToolResults[0].GetContent())
}

// A TEMPORAL ACTIVITY RETRY MUST NOT RE-RUN THE TOOL.
//
// This is the one re-entry checkPriorTerminalResult structurally cannot catch.
// A worker that died mid-tool (crash, OOM, heartbeat timeout) never wrote a
// terminal row, so the call is still EXECUTING — which is deliberately NOT
// terminal, because that is also exactly what a healthy in-flight call looks
// like. Temporal then re-delivers the SAME activity task with Attempt
// incremented, and nothing else stops the tool running a second time.
//
// Tools are not idempotent. `ExecuteRunStep` has refused retries this way for
// shell commands since long before this (run_step.go:105); this pins the same
// protection for every tool.
//
// The SDK's TestActivityEnvironment cannot set Attempt, so this asserts the
// guard's decision directly rather than through the harness.
func TestExecuteTools_ActivityRetryDoesNotReExecute(t *testing.T) {
	// Attempt 1 is a first delivery: the tool must run.
	assert.False(t, isActivityRetry(1),
		"attempt 1 is the first delivery, not a retry — the tool must execute")

	// Attempt 2+ means Temporal re-delivered a task that already ran once.
	for _, attempt := range []int{2, 3, 5} {
		assert.True(t, isActivityRetry(attempt),
			"attempt %d is a redelivery; the tool already ran and must not run again", attempt)
	}
}

// The retry guard reports the interruption as an ERROR TOOL RESULT on a
// SUCCESSFUL activity, rather than failing the activity.
//
// Failing would burn the remaining attempts (MaximumAttempts: 5) and
// eventually kill the step. The honest outcome is a completed activity
// carrying an error result: the loop advances, and the model is told the tool
// was interrupted so it can decide whether to try again itself.
func TestExecuteTools_ActivityRetryResultIsAnErrorNotAFailure(t *testing.T) {
	activityInstance := &ExecuteToolsActivity{}
	result := activityInstance.buildToolResult("toolu_retry", "bash", InterruptedToolResultContent, "", true, nil)

	assert.True(t, result.IsError, "the model must see that this tool did not produce a real result")
	assert.Equal(t, InterruptedToolResultContent, result.Content,
		"the result must say the outcome is unknown, so effects are verified before re-running")
}

// THE LIVE EVENT MUST NAME THE SAME OUTCOME AS THE DURABLE ROW.
//
// The UI reads the live chat_updates event while the chat is open and the
// durable tool_calls row on reload. When they disagree the same tool renders
// green now and orange later — which is exactly what a user saw after
// interrupting a bash: "Completed" on screen, "Warning" when they came back.
//
// The cause was an unconditional emitToolStatus(..., "completed") issued
// BEFORE the status was computed, while the row was then written Failed for a
// tool whose result was an error. Deriving both from one value is what keeps
// them from drifting again.
func TestToolStatusEvent_MatchesTheDurableStatus(t *testing.T) {
	cases := map[core.ToolCallStatus]string{
		core.ToolCallStatusCompleted:    "completed",
		core.ToolCallStatusFailed:       "failed",
		core.ToolCallStatusCancelled:    "cancelled",
		core.ToolCallStatusBackgrounded: "backgrounded",
	}
	for status, want := range cases {
		assert.Equal(t, want, toolStatusEvent(status),
			"the event for a %v row must name that same outcome", status)
	}
}

// A tool that RAN but returned an error is Failed, not Completed — and the
// live event must say so. core.ToolCallStatusFailed is defined as "the tool
// ran and returned an error result", so a green "completed" here is the exact
// lie that produced the green-then-orange flip.
func TestToolStatusEvent_ErrorResultIsNotAnnouncedAsCompleted(t *testing.T) {
	assert.NotEqual(t, "completed", toolStatusEvent(core.ToolCallStatusFailed),
		"a tool whose result was an error must not be announced as completed")
	assert.NotEqual(t, "completed", toolStatusEvent(core.ToolCallStatusCancelled),
		"a cancelled tool must not be announced as completed")
}
