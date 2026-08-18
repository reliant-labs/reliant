// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are about DURABILITY, not about the chat_updates event stream.
// Every assertion reads the tool_calls / tool_call_results tables back through
// the repository AFTER the activity has returned -- which is exactly what a
// page reload does, and exactly what was impossible before this slice: status
// existed only as a transient event that a reloading client had already missed.

// durableStatusFixture is the parent-row setup a tool call needs. tool_calls
// has an FK to chats, so a chat must exist before any call can be written.
type durableStatusFixture struct {
	h      *IdempotencyTestHelper
	chatID string
}

func setupDurableStatusFixture(t *testing.T) *durableStatusFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	// Creates the chat AND its root thread + context window.
	h.CreateTestChat(ctx, chatID, projectID, userID)

	return &durableStatusFixture{h: h, chatID: chatID}
}

func (f *durableStatusFixture) executeTool(t *testing.T, toolCallID, toolName, toolInput string, executor *mockToolExecutor) *ExecuteToolsOutput {
	t.Helper()
	activityInstance := NewExecuteToolsActivity(f.h.Repo(), executor)

	var output ExecuteToolsOutput
	err := f.h.ExecuteActivity(activityInstance.Execute, ExecuteToolsInput{
		ChatID: f.chatID,
		Thread: f.chatID,
		ToolCalls: []message.ToolCall{
			{ID: toolCallID, Name: toolName, Input: toolInput},
		},
	}, &output)
	require.NoError(t, err)
	return &output
}

// A tool that ran to completion leaves a COMPLETED call and a matching result
// row. The pair is what a reload needs: the status alone can't render the tool
// card's output.
func TestDurableStatus_CompletedToolPersistsCallAndResult(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{
		Success: true,
		Content: "total 0\ndrwxr-xr-x  2 user  staff",
	})

	output := f.executeTool(t, toolCallID, "bash", `{"command":"ls -la"}`, executor)
	require.Len(t, output.ToolResults, 1)
	require.False(t, output.ToolResults[0].GetIsError())

	// The call row.
	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err, "a completed tool must leave a durable tool_calls row")
	assert.Equal(t, core.ToolCallStatusCompleted, call.Status)
	assert.Equal(t, f.chatID, call.ChatID)
	assert.Equal(t, "bash", call.ToolName)
	assert.JSONEq(t, `{"command":"ls -la"}`, string(call.Input))
	require.NotNil(t, call.CompletedAt, "COMPLETED without completed_at violates the CHECK constraint")
	require.NotNil(t, call.StartedAt, "started_at is set by the EXECUTING transition and must survive")
	assert.Nil(t, call.ErrorMessage)

	// The result row, and — the point of writing it — the content the LLM saw.
	result := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, result, "a completed tool must leave a durable tool_call_results row")
	assert.False(t, result.IsError)
	assert.Equal(t, output.ToolResults[0].GetContent(), result.Content,
		"the durable result must be the same content that went to the LLM")
}

// A tool whose executor returned an error records FAILED plus the error text,
// so a reload can say why rather than showing a call stuck at "executing".
func TestDurableStatus_FailedToolRecordsError(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetError(toolCallID, errors.New("daemon unreachable"))

	output := f.executeTool(t, toolCallID, "bash", `{"command":"ls"}`, executor)
	require.Len(t, output.ToolResults, 1)
	require.True(t, output.ToolResults[0].GetIsError())

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusFailed, call.Status)
	require.NotNil(t, call.ErrorMessage)
	assert.Contains(t, *call.ErrorMessage, "daemon unreachable")
	require.NotNil(t, call.CompletedAt)

	// A failure still produces a result the LLM sees, so it still needs a row:
	// an assistant tool_use with no matching tool_result deadlocks the provider.
	result := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, result, "a failed tool still owes the conversation a result row")
	assert.True(t, result.IsError)
	assert.Equal(t, output.ToolResults[0].GetContent(), result.Content)
}

// A tool that ran fine but returned a business-level error (bad arguments, no
// matches, etc.) is FAILED too: core.ToolCallStatusFailed is defined as "the
// tool ran and returned an error result", and the chat_updates event stream
// reports both of these as plain "completed".
func TestDurableStatus_ToolReportedErrorIsFailed(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{
		Success: true,
		IsError: true,
		Content: "grep: invalid pattern",
	})

	f.executeTool(t, toolCallID, "grep", `{"pattern":"["}`, executor)

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusFailed, call.Status)

	result := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Equal(t, "grep: invalid pattern", result.Content)
}

// A backgrounded tool is still running somewhere, so it gets BACKGROUNDED and
// NO result row — it hasn't produced one yet.
func TestDurableStatus_BackgroundedToolHasNoResultYet(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{
		Success:      true,
		Backgrounded: true,
		Content:      `{"process_id":"proc-1","backgrounded":true}`,
	})

	f.executeTool(t, toolCallID, "bash", `{"command":"npm run dev"}`, executor)

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusBackgrounded, call.Status)
	assert.Nil(t, call.CompletedAt, "a backgrounded call has not completed")
	require.NotNil(t, call.StartedAt)

	assert.Nil(t, getToolCallResult(t, f.h, toolCallID),
		"a backgrounded tool has not produced a result yet")
}

// Temporal retries activities. A retry must converge on the same single row
// rather than duplicating it or corrupting the status — that is why every
// write here is an upsert keyed on the tool call id.
func TestDurableStatus_ActivityRetryDoesNotDuplicateRows(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{
		Success: true,
		Content: "ok",
	})

	// Run the same activity input three times, as a retrying activity would.
	for i := 0; i < 3; i++ {
		f.executeTool(t, toolCallID, "bash", `{"command":"ls"}`, executor)
	}

	calls, err := f.h.Repo().ListToolCallsByChat(ctx, f.chatID)
	require.NoError(t, err)
	require.Len(t, calls, 1, "a retried activity must converge on one row, not append")
	assert.Equal(t, toolCallID, calls[0].ID)
	assert.Equal(t, core.ToolCallStatusCompleted, calls[0].Status,
		"the terminal status must survive a retry intact")
	require.NotNil(t, calls[0].CompletedAt)

	result := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, result)
	assert.Equal(t, "ok", result.Content)
}

// Two tools in one activity invocation each get their own row.
func TestDurableStatus_ParallelToolsEachGetARow(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	firstID := "toolu_" + uuid.New().String()
	secondID := "toolu_" + uuid.New().String()

	executor := newMockToolExecutor()
	executor.SetResult(firstID, &toolexec.ToolResult{Success: true, Content: "first"})
	executor.SetError(secondID, errors.New("second blew up"))

	activityInstance := NewExecuteToolsActivity(f.h.Repo(), executor)
	var output ExecuteToolsOutput
	require.NoError(t, f.h.ExecuteActivity(activityInstance.Execute, ExecuteToolsInput{
		ChatID: f.chatID,
		Thread: f.chatID,
		ToolCalls: []message.ToolCall{
			{ID: firstID, Name: "bash", Input: `{"command":"a"}`},
			{ID: secondID, Name: "bash", Input: `{"command":"b"}`},
		},
	}, &output))

	calls, err := f.h.Repo().ListToolCallsByChat(ctx, f.chatID)
	require.NoError(t, err)
	require.Len(t, calls, 2)

	byID := map[string]*core.ToolCall{}
	for _, c := range calls {
		byID[c.ID] = c
	}
	require.Contains(t, byID, firstID)
	require.Contains(t, byID, secondID)
	assert.Equal(t, core.ToolCallStatusCompleted, byID[firstID].Status)
	assert.Equal(t, core.ToolCallStatusFailed, byID[secondID].Status)
}

// Tool input that isn't valid JSON is stored as NULL rather than failing the
// call: input is a best-effort record, and the column is jsonb.
func TestDurableStatus_UnparseableInputStoredAsNull(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()

	// executeSingleTool rejects unparseable input before dispatch, so the
	// helper is exercised directly for the malformed case.
	assert.Nil(t, toolInputToJSON("not json at all"))
	assert.Nil(t, toolInputToJSON(""))
	assert.NotNil(t, toolInputToJSON(`{"ok":true}`))

	f.executeTool(t, toolCallID, "bash", `{"command":"ls"}`, executor)
	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"command":"ls"}`, string(call.Input))
}

// getToolCallResult reads a call's result row back, returning nil when there
// is none. Results are looked up by the batch message-id query, so this walks
// the chat's calls to find the one under test.
// handleResultWithCancelledCtx drives handleToolExecutionResult with an
// already-cancelled context, standing in for a sibling tool's cancellation
// tearing down the activity context this call shares with it.
//
// It runs inside a real activity so the Temporal activity logger and the
// detached terminal-write path behave as they do in production; the wrapper
// activity cancels its own context before delegating.
func (f *durableStatusFixture) handleResultWithCancelledCtx(
	t *testing.T,
	tec *toolExecutionContext,
	execResult *toolexec.ToolResult,
	execErr error,
) *reliantv1.ToolResultMsg {
	t.Helper()
	activityInstance := NewExecuteToolsActivity(f.h.Repo(), newMockToolExecutor())

	run := func(ctx context.Context, _ string) (*reliantv1.ToolResultMsg, error) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		out := activityInstance.handleToolExecutionResult(cancelled, tec, execResult, execErr, time.Now())
		return messageToolResultsToProto([]message.ToolResult{out})[0], nil
	}

	var got reliantv1.ToolResultMsg
	require.NoError(t, f.h.ExecuteActivity(run, tec.toolCallID, &got))
	return &got
}

// A cancelled context says nothing about THIS tool.
//
// Every tool call in an LLM turn runs as a parallel goroutine inside one
// ExecuteTools activity, sharing a single context. Cancelling one tool used to
// cancel the whole chat workflow, which killed that shared context, so every
// sibling arrived here with ctx.Err() != nil. Because the cancellation check
// ran before the result was even looked at, a tool that had already finished
// successfully was reported to the user as cancelled and its real output was
// thrown away.
//
// The rule these two cases pin: a finished execution is reported on its own
// merits; cancellation only decides the outcome of a call that has none.
func TestDurableStatus_CancelledContextDoesNotDiscardCompletedSibling(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	toolCallID := "toolu_" + uuid.New().String()
	tec := &toolExecutionContext{
		chatID:     f.chatID,
		thread:     f.chatID,
		toolName:   "bash",
		toolInput:  `{"command":"ls"}`,
		toolCallID: toolCallID,
	}

	// The sibling that finished before the cancel landed.
	result := f.handleResultWithCancelledCtx(t, tec, &toolexec.ToolResult{
		Success: true,
		Content: "total 0\ndrwxr-xr-x  2 user  staff",
	}, nil)

	assert.False(t, result.GetIsError(),
		"a tool that completed must not be reported as an error because a sibling was cancelled")
	assert.Contains(t, result.GetContent(), "drwxr-xr-x",
		"the completed tool's real output must reach the LLM, not a cancellation notice")

	call, err := f.h.Repo().GetToolCall(context.Background(), toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCompleted, call.Status,
		"a completed tool must persist as COMPLETED even when the shared context is dead")
	persisted := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, persisted, "terminal result must persist even when the shared context is dead")
	assert.Equal(t, result.GetContent(), persisted.Content)
}

// The other half of the rule: a call that was still in flight when the context
// died has no outcome of its own, so cancellation is the honest answer.
func TestDurableStatus_CancelledContextStillCancelsUnfinishedTool(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	toolCallID := "toolu_" + uuid.New().String()
	tec := &toolExecutionContext{
		chatID:     f.chatID,
		thread:     f.chatID,
		toolName:   "bash",
		toolInput:  `{"command":"sleep 60"}`,
		toolCallID: toolCallID,
	}

	result := f.handleResultWithCancelledCtx(t, tec, nil, nil)

	assert.True(t, result.GetIsError())
	assert.Contains(t, result.GetContent(), "cancelled")

	call, err := f.h.Repo().GetToolCall(context.Background(), toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCancelled, call.Status)
	persisted := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, persisted, "a cancelled terminal tool still owes the provider a persisted result")
	assert.True(t, persisted.IsError)
	assert.Contains(t, persisted.Content, "cancelled")
}

func getToolCallResult(t *testing.T, h *IdempotencyTestHelper, toolCallID string) *core.ToolCallResult {
	t.Helper()
	ctx := context.Background()

	row := h.DB().QueryRowContext(ctx,
		`SELECT tool_call_id, content, is_error FROM tool_call_results WHERE tool_call_id = $1`,
		toolCallID)

	var result core.ToolCallResult
	if err := row.Scan(&result.ToolCallID, &result.Content, &result.IsError); err != nil {
		return nil
	}
	return &result
}
