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
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
	"github.com/stretchr/testify/require"
)

func TestCancelToolCall_DeliversCancelToDaemonAndSucceeds(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	blockID := uuid.New().String()
	toolCallID := "tool-call-1"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "Cancel test",
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
		Version:    toolCallTestIntPtr(1),
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	router := &toolexectest.Recorder{}
	svc := NewToolCallService(repo, nil, router)

	resp, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{ToolCallId: toolCallID}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	require.Len(t, router.CancelCalls, 1)
	require.Equal(t, toolCallID, router.CancelCalls[0].RequestID)
}

func toolCallTestIntPtr(v int) *int { return &v }
