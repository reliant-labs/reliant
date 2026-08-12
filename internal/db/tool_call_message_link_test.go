package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// seedToolCallBlock creates a chat/thread/message with one tool_call block and
// returns the ids. This is the state the world is in when a live tool-call
// writer runs: the assistant message and its block are already persisted, and
// the tool_calls row does not exist yet.
func seedToolCallBlock(t *testing.T, repo *Repo, ctx context.Context, toolCallID, toolName string) (chatID, threadID, messageID string) {
	t.Helper()
	now := time.Now().UTC()

	chatID = uuid.New().String()
	threadID = uuid.New().String()
	cwID := uuid.New().String()
	messageID = uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "link", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &MessageContentBlock{
		ID: uuid.New().String(), MessageID: messageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(toolCallID),
		ToolName:   ptr.Of(toolName),
		Version:    ptr.Of(1),
		CreatedAt:  now, UpdatedAt: now,
	}))
	return chatID, threadID, messageID
}

// TestUpsertToolCallStatus_ResolvesMessageIDFromBlock is the fix for the bug
// that kept a running spawn's preview on "Starting…".
//
// A live writer (the execute_tools activity, or EmitToolCallStatus on the
// spawn path) is workflow-side and has no message id to supply. Its row landed
// with message_id NULL, and every read path enriches tool-call blocks via
// `WHERE message_id IN (...)`, which cannot match a NULL. So the block shipped
// without the child_workflow_id naming the spawn's thread.
//
// The link is recoverable because the block is written BEFORE the call row.
func TestUpsertToolCallStatus_ResolvesMessageIDFromBlock(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	toolCallID := "toolu_" + uuid.New().String()[:8]
	chatID, threadID, messageID := seedToolCallBlock(t, repo, ctx, toolCallID, "spawn")
	childThreadID := uuid.New().String()

	// Exactly what the spawn path writes: no MessageID, no ThreadID.
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID: toolCallID, ChatID: chatID, ToolName: "spawn",
		Status:          core.ToolCallStatusExecuting,
		ChildWorkflowID: &childThreadID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	stored, err := repo.GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, stored.MessageID,
		"a writer with no message must still produce a row the read paths can find")
	require.Equal(t, messageID, *stored.MessageID)
	require.NotNil(t, stored.ThreadID)
	require.Equal(t, threadID, *stored.ThreadID)

	// The point of the link: the by-message read now sees the row, and with it
	// the child_workflow_id the spawn preview needs.
	calls, err := repo.ListToolCallsByMessageIDs(ctx, []string{messageID})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].ChildWorkflowID)
	require.Equal(t, childThreadID, *calls[0].ChildWorkflowID)
}

// A caller that DOES know the message must win: resolution fills gaps, it
// never overrides what a writer asserted.
func TestUpsertToolCallStatus_DoesNotOverrideProvidedMessageID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	toolCallID := "toolu_" + uuid.New().String()[:8]
	chatID, _, messageID := seedToolCallBlock(t, repo, ctx, toolCallID, "bash")

	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID: toolCallID, ChatID: chatID, ToolName: "bash",
		Status:      core.ToolCallStatusExecuting,
		MessageID:   &messageID,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	stored, err := repo.GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	require.Equal(t, messageID, *stored.MessageID)
}

// No block yet (a call written before its block, or a block that never
// existed) must not fail the write. Status bookkeeping is best-effort and must
// never break the tool call it describes.
func TestUpsertToolCallStatus_NoBlockIsNotAnError(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "no block", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	toolCallID := "toolu_" + uuid.New().String()[:8]
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID: toolCallID, ChatID: chatID, ToolName: "bash",
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	stored, err := repo.GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	require.Nil(t, stored.MessageID)
}

// ListToolCallsByIDs is the read that does not depend on message_id at all —
// the "join on both" half. Even a row whose message link is missing must be
// reachable by the id its block carries.
func TestListToolCallsByIDs_FindsRowWithoutMessageLink(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "by id", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	toolCallID := "toolu_" + uuid.New().String()[:8]
	childThreadID := uuid.New().String()
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID: toolCallID, ChatID: chatID, ToolName: "spawn",
		Status:          core.ToolCallStatusExecuting,
		ChildWorkflowID: &childThreadID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	// The by-message read cannot see it — that is the failure mode.
	byMessage, err := repo.ListToolCallsByMessageIDs(ctx, []string{uuid.New().String()})
	require.NoError(t, err)
	require.Empty(t, byMessage)

	// The by-id read can.
	byID, err := repo.ListToolCallsByIDs(ctx, []string{toolCallID})
	require.NoError(t, err)
	require.Len(t, byID, 1)
	require.Equal(t, childThreadID, *byID[0].ChildWorkflowID)
}

func TestListToolCallsByIDs_EmptyInput(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	calls, err := repo.ListToolCallsByIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, calls)
}
