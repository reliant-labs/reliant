package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cancelling or backgrounding a tool call is a user action taken from the UI.
// Before this slice it produced only a transient chat_updates event, so a
// reload showed the call as if nothing had happened. These tests read the row
// back after the RPC returns — the reload's view.

type toolCallDurableFixture struct {
	repo       *db.Repo
	chatID     string
	threadID   string
	messageID  string
	toolCallID string
}

func setupToolCallDurableFixture(t *testing.T, ctx context.Context, repo *db.Repo, toolInput string) toolCallDurableFixture {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	blockID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()
	toolName := "bash"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "durable status",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              messageID,
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         blockID,
		MessageID:  messageID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		Version:    toolCallTestIntPtr(1),
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	return toolCallDurableFixture{
		repo:       repo,
		chatID:     chatID,
		threadID:   threadID,
		messageID:  messageID,
		toolCallID: toolCallID,
	}
}

func TestCancelToolCall_PersistsCancelledStatus(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := setupToolCallDurableFixture(t, ctx, repo, `{"command":"sleep 600"}`)

	svc := NewToolCallService(repo, nil, &fakeDaemonRouter{})
	resp, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: f.toolCallID,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	call, err := repo.GetToolCall(ctx, f.toolCallID)
	require.NoError(t, err, "cancelling must leave a durable row, not just an event")
	assert.Equal(t, core.ToolCallStatusCancelled, call.Status)
	assert.Equal(t, f.chatID, call.ChatID)
	assert.Equal(t, "bash", call.ToolName)
	require.NotNil(t, call.CompletedAt, "a cancelled call is terminal and will never produce a result")

	// This handler already looked up the block and message to get here, so the
	// attribution columns are free — and worth having.
	require.NotNil(t, call.MessageID)
	assert.Equal(t, f.messageID, *call.MessageID)
	require.NotNil(t, call.ThreadID)
	assert.Equal(t, f.threadID, *call.ThreadID)
	assert.JSONEq(t, `{"command":"sleep 600"}`, string(call.Input))
}

func TestConvertToBackground_PersistsBackgroundedStatus(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := setupToolCallDurableFixture(t, ctx, repo, `{"command":"npm run dev"}`)

	svc := NewToolCallService(repo, nil, &fakeDaemonRouter{})
	resp, err := svc.ConvertToBackground(ctx, connect.NewRequest(&reliantv1.ConvertToBackgroundRequest{
		ToolCallId: f.toolCallID,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	call, err := repo.GetToolCall(ctx, f.toolCallID)
	require.NoError(t, err, "backgrounding must leave a durable row")
	assert.Equal(t, core.ToolCallStatusBackgrounded, call.Status)
	assert.Nil(t, call.CompletedAt, "a backgrounded call is still running elsewhere")
	assert.JSONEq(t, `{"command":"npm run dev"}`, string(call.Input))
}

// The handler runs after the execution path has already written the call, so
// it must update that row in place rather than blanking the timing the
// activity recorded.
func TestCancelToolCall_PreservesExistingExecutionFields(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := setupToolCallDurableFixture(t, ctx, repo, `{"command":"sleep 600"}`)

	// The execute_tools activity got there first.
	startedAt := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCall(ctx, &core.ToolCall{
		ID:          f.toolCallID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusExecuting,
		StartedAt:   &startedAt,
		RequestedAt: startedAt,
		CreatedAt:   startedAt,
		UpdatedAt:   startedAt,
	}))

	svc := NewToolCallService(repo, nil, &fakeDaemonRouter{})
	_, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: f.toolCallID,
	}))
	require.NoError(t, err)

	call, err := repo.GetToolCall(ctx, f.toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCancelled, call.Status)
	require.NotNil(t, call.StartedAt, "the activity's started_at must survive a later cancel")
	assert.Equal(t, startedAt.Unix(), call.RequestedAt.Unix(),
		"requested_at belongs to the first write and must not move")

	calls, err := repo.ListToolCallsByChat(ctx, f.chatID)
	require.NoError(t, err)
	require.Len(t, calls, 1, "the handler must update the existing row, not add one")
}

// A repeated cancel (double-click, retried request) must converge.
func TestCancelToolCall_IsIdempotent(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := setupToolCallDurableFixture(t, ctx, repo, `{"command":"sleep 600"}`)

	svc := NewToolCallService(repo, nil, &fakeDaemonRouter{})
	for i := 0; i < 3; i++ {
		_, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
			ToolCallId: f.toolCallID,
		}))
		require.NoError(t, err)
	}

	calls, err := repo.ListToolCallsByChat(ctx, f.chatID)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, core.ToolCallStatusCancelled, calls[0].Status)
}
