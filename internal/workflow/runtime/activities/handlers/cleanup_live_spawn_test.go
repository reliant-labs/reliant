// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// seedSpawnToolCall creates the state a running spawn is in: an assistant
// message carrying a spawn tool_call block, a durable tool_calls row at
// EXECUTING naming its child workflow, and the child workflow itself at the
// given status. No tool_result block — a spawn writes that only at the end.
func seedSpawnToolCall(
	t *testing.T,
	repo db.Repository,
	ctx context.Context,
	chatID, contextWindowID string,
	callStatus core.ToolCallStatus,
	childStatus core.WorkflowStatus,
) (toolCallID, childWorkflowID string) {
	t.Helper()
	now := time.Now()

	msgID := uuid.New().String()
	require.NoError(t, createMessageWithSeq(ctx, t, repo, &db.Message{
		ID: msgID, ChatID: chatID, Ordinal: 1, ThreadID: chatID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now, UpdatedAt: now,
	}))

	toolCallID = "toolu_" + uuid.New().String()
	childWorkflowID = uuid.New().String()
	toolName := "spawn"
	toolInput := `{"preset":"general","prompt":"do work"}`
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		ToolCallID: &toolCallID,
		CreatedAt:  now, UpdatedAt: now,
	}))

	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, MessageID: &msgID,
		ToolName: "spawn", Status: callStatus,
		ChildWorkflowID: &childWorkflowID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childWorkflowID, Status: childStatus, CreatedAt: now,
	}))

	return toolCallID, childWorkflowID
}

// TestCleanup_DoesNotRepairRunningSpawn is the regression test for the
// corruption this fix targets.
//
// Cleanup runs from handleWorkflowCompletion on the abnormal paths — cancelled,
// panic, error — i.e. exactly when a worker has crashed. Its orphan test used
// to be "this tool_call has no tool_result block", which for a spawn is not
// evidence of death: the spawn's result is written minutes later by a separate
// workflow on a separate thread that the parent's crash never touched.
//
// Reconstructed from production data: a parent hit a terminal path at 04:54:50
// while two spawn children were writing messages every few seconds. Cleanup
// declared both orphaned and wrote "interrupted — outcome unknown". Both
// children then completed SUCCESSFULLY (05:02, 05:05) and wrote their real
// results. Every affected call ended up with two tool_result blocks — one
// fabricated failure, one genuine success — and the tool-pairing validator had
// to repair the history in memory on every load afterwards.
func TestCleanup_DoesNotRepairRunningSpawn(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, cwID := createTestChatWithContextWindow(t, repo)

	toolCallID, _ := seedSpawnToolCall(t, repo, ctx, chatID, cwID,
		core.ToolCallStatusExecuting, core.WorkflowStatusRunning)

	output, err := NewCleanupActivity(repo).Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 0, output.ToolCallsCancelled,
		"a spawn that is still executing must not be declared orphaned")

	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.Nil(t, resultBlock,
		"no fabricated result may be written for a live call — the real one is still coming, and both would then exist")
}

// A spawn whose child workflow has genuinely finished without ever writing a
// result IS an orphan, and must still be repaired. Otherwise the fix trades one
// broken conversation for another: an unanswered tool_use deadlocks the
// provider.
func TestCleanup_RepairsSpawnWhoseChildWorkflowEnded(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, cwID := createTestChatWithContextWindow(t, repo)

	// Call row still says EXECUTING (its terminal write was lost — the very
	// thing a crash does), but the child workflow is over.
	toolCallID, _ := seedSpawnToolCall(t, repo, ctx, chatID, cwID,
		core.ToolCallStatusExecuting, core.WorkflowStatusFailed)

	output, err := NewCleanupActivity(repo).Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 1, output.ToolCallsCancelled,
		"a spawn whose child workflow has ended with no result is a real orphan")

	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock, "the conversation must not be left with an unanswered tool_use")
}

// A paused workflow resumes and finishes its work, so its tool calls are still
// live. Treating PAUSED as terminal would fabricate failures for every tool
// call in a paused chat.
func TestCleanup_DoesNotRepairPausedSpawn(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, cwID := createTestChatWithContextWindow(t, repo)

	toolCallID, _ := seedSpawnToolCall(t, repo, ctx, chatID, cwID,
		core.ToolCallStatusExecuting, core.WorkflowStatusPaused)

	output, err := NewCleanupActivity(repo).Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 0, output.ToolCallsCancelled, "a paused workflow will resume; its calls are not orphaned")

	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.Nil(t, resultBlock)
}

// A call whose durable row records a terminal status but which never produced a
// result block is an orphan — the outcome is known to be over.
func TestCleanup_RepairsTerminalCallWithNoResult(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, cwID := createTestChatWithContextWindow(t, repo)

	toolCallID, _ := seedSpawnToolCall(t, repo, ctx, chatID, cwID,
		core.ToolCallStatusCancelled, core.WorkflowStatusCancelled)

	output, err := NewCleanupActivity(repo).Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 1, output.ToolCallsCancelled)

	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock)
}

// A non-spawn tool call that is still EXECUTING must also be left alone. The
// window is much smaller for an in-process tool, but "no result yet" means the
// same thing whatever the tool is, and a crash mid-bash produces exactly this.
func TestCleanup_DoesNotRepairExecutingRegularTool(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, cwID := createTestChatWithContextWindow(t, repo)
	now := time.Now()

	msgID := uuid.New().String()
	require.NoError(t, createMessageWithSeq(ctx, t, repo, &db.Message{
		ID: msgID, ChatID: chatID, Ordinal: 1, ThreadID: chatID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now, UpdatedAt: now,
	}))

	toolCallID := "toolu_" + uuid.New().String()
	toolName := "bash"
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolCallID: &toolCallID,
		CreatedAt:  now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, MessageID: &msgID,
		ToolName: "bash", Status: core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	output, err := NewCleanupActivity(repo).Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 0, output.ToolCallsCancelled)
}

// A tool call with NO durable row at all keeps the old behaviour: nothing
// claims it is running, so the missing result is the only evidence there is.
// This is the pre-tool_calls-table history, and it must still be repairable.
func TestCleanup_RepairsCallWithNoDurableRow(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, cwID := createTestChatWithContextWindow(t, repo)
	now := time.Now()

	msgID := uuid.New().String()
	require.NoError(t, createMessageWithSeq(ctx, t, repo, &db.Message{
		ID: msgID, ChatID: chatID, Ordinal: 1, ThreadID: chatID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now, UpdatedAt: now,
	}))

	toolCallID := "toolu_" + uuid.New().String()
	toolName := "bash"
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolCallID: &toolCallID,
		CreatedAt:  now, UpdatedAt: now,
	}))

	output, err := NewCleanupActivity(repo).Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 1, output.ToolCallsCancelled,
		"with no durable row there is nothing asserting the call is live")
}
