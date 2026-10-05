// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A backgrounded call must record WHERE its process runs.
//
// The process lives in one daemon's memory, and only that daemon can say when
// it ends. Before this, the row kept neither the process id nor the daemon, so
// nothing could ever ask — and every backgrounded shell call stayed at status 6
// forever (4,213 of 4,224 backgrounded rows on the dev database had a NULL
// background_process_id, and none had a way to name their daemon).
func TestDurableStatus_BackgroundedToolRecordsWhereItRuns(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{
		Success:      true,
		Backgrounded: true,
		Content:      `{"process_id":"proc-42","command":"npm run dev","backgrounded":true}`,
		// The shell tool reports the process id in its structured metadata;
		// that is the value the row must keep.
		Metadata: `{"start_time":1,"end_time":2,"process_id":"proc-42"}`,
		DaemonID: "daemon-7",
	})

	f.executeTool(t, toolCallID, "shell", `{"command":"npm run dev","run_in_background":true}`, executor)

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusBackgrounded, call.Status)
	require.NotNil(t, call.BackgroundProcessID, "a backgrounded call must name its process, or nothing can ever learn it ended")
	assert.Equal(t, "proc-42", *call.BackgroundProcessID)
	require.NotNil(t, call.DaemonID, "a backgrounded call must name the daemon its process lives on")
	assert.Equal(t, "daemon-7", *call.DaemonID)
}

// executeSingleToolAsRetry runs one tool call as Temporal's re-delivered
// attempt 2. The SDK's TestActivityEnvironment cannot set Attempt, so the
// attempt number is passed straight to executeSingleTool — inside a registered
// activity, so the activity logger and context it relies on are real.
func executeSingleToolAsRetry(t *testing.T, f *durableStatusFixture, executor *mockToolExecutor, toolCallID, toolName, toolInput string) message.ToolResult {
	t.Helper()
	activityInstance := NewExecuteToolsActivity(f.h.Repo(), executor)

	runRetry := func(ctx context.Context) (message.ToolResult, error) {
		return activityInstance.executeSingleTool(ctx, f.chatID, f.chatID, toolName, toolInput, toolCallID,
			"act-retry", "run-retry", 2, "", nil), nil
	}
	f.h.env.RegisterActivity(runRetry)
	val, err := f.h.env.ExecuteActivity(runRetry)
	require.NoError(t, err)

	var result message.ToolResult
	require.NoError(t, val.Get(&result))
	return result
}

// A Temporal activity retry tells the model the tool was interrupted — and must
// say the same thing durably.
//
// The redelivered attempt never re-runs the tool (a crashed worker may have
// done some, all or none of its work), so it returns the "interrupted" result
// instead. It used to return it WITHOUT touching the row, which stayed at
// EXECUTING forever: observed on chat 8bb0a875, three shell calls from
// 01:46-01:49 still "executing" two days later, each carrying exactly that
// interrupted result block. The UI renders such a row as a running tool with a
// live Cancel button.
func TestExecuteTools_ActivityRetryClosesTheRow(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	startedAt := time.Now().Add(-2 * time.Minute).UTC()
	// Attempt 1 got this far before its worker died.
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      f.chatID,
		ToolName:    "shell",
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: startedAt,
		StartedAt:   &startedAt,
		CreatedAt:   startedAt,
		UpdatedAt:   startedAt,
	}))

	executor := newMockToolExecutor()
	result := executeSingleToolAsRetry(t, f, executor, toolCallID, "shell", `{"command":"sleep 150"}`)

	assert.Equal(t, 0, executor.GetExecutionCount(toolCallID), "a retry must not re-run the tool")
	assert.True(t, result.IsError)
	assert.Equal(t, InterruptedToolResultContent, result.Content)

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.True(t, call.Status.IsTerminal(),
		"the model was told this call is over; the row must not keep saying it is executing (got status %d)", call.Status)
	assert.Equal(t, core.ToolCallStatusFailed, call.Status)
	require.NotNil(t, call.CompletedAt)
	require.NotNil(t, call.StartedAt, "the retry's write must not erase when attempt 1 started")
	assert.WithinDuration(t, startedAt, *call.StartedAt, time.Second)

	durable := getToolCallResult(t, f.h, toolCallID)
	require.NotNil(t, durable, "the result the model saw must be the durable result")
	assert.True(t, durable.IsError)
	assert.Equal(t, InterruptedToolResultContent, durable.Content)
}

// A retry that lands on a call attempt 1 already BACKGROUNDED must leave it
// backgrounded: that process is still running, and the reconciler closes the
// row from the process's real outcome. Writing FAILED here would report a live
// dev server as dead.
func TestExecuteTools_ActivityRetryLeavesABackgroundedRowAlone(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_" + uuid.New().String()
	now := time.Now().UTC()
	processID := "proc-still-running"
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID:                  toolCallID,
		ChatID:              f.chatID,
		ToolName:            "shell",
		Status:              core.ToolCallStatusBackgrounded,
		BackgroundProcessID: &processID,
		RequestedAt:         now,
		StartedAt:           &now,
		CreatedAt:           now,
		UpdatedAt:           now,
	}))

	executeSingleToolAsRetry(t, f, newMockToolExecutor(), toolCallID, "shell", `{"command":"npm run dev"}`)

	call, err := f.h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusBackgrounded, call.Status)
	require.NotNil(t, call.BackgroundProcessID)
	assert.Equal(t, processID, *call.BackgroundProcessID)
}
