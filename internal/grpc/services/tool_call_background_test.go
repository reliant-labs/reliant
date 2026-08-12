// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

func seedRunningToolCall(t *testing.T, repo *db.Repo, ctx context.Context) (chatID, toolCallID string) {
	t.Helper()
	now := time.Now().UTC()

	chatID = uuid.New().String()
	threadID := chatID
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	toolCallID = "toolu_" + uuid.New().String()[:8]

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "background", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: messageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		ToolName:   ptr.Of("bash"),
		Version:    ptr.Of(1),
		CreatedAt:  now, UpdatedAt: now,
	}))
	return chatID, toolCallID
}

// Backgrounding must actually REACH the daemon, because that is where the
// command runs. Before this, ConvertToBackground set an in-memory flag in the
// API server that nothing ever read (shell.BackgroundSignal.IsBackgrounded had
// zero callers), wrote status=BACKGROUNDED, and returned success — while the
// command kept running in the foreground. Every backgrounded row in the
// database has a NULL background_process_id as a result.
func TestConvertToBackground_SendsRequestToDaemon(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	_, toolCallID := seedRunningToolCall(t, repo, ctx)

	router := &fakeDaemonRouter{}
	svc := NewToolCallService(repo, nil, router)

	resp, err := svc.ConvertToBackground(ctx, connect.NewRequest(&reliantv1.ConvertToBackgroundRequest{
		ToolCallId: toolCallID,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	require.Len(t, router.backgroundCalls, 1,
		"the request must cross the wire — the command runs in the daemon, not here")
	require.Equal(t, toolCallID, router.backgroundCalls[0].toolCallID)
	require.Equal(t, toolCallID, router.backgroundCalls[0].requestID,
		"addressed by tool call id: the only id the UI can name")
}

// When the daemon cannot be reached the command is still running in the
// foreground, so the RPC must fail rather than report success. Claiming success
// here is what made a missing feature look like a working one.
func TestConvertToBackground_DaemonUnreachable_DoesNotClaimSuccess(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	_, toolCallID := seedRunningToolCall(t, repo, ctx)

	router := &fakeDaemonRouter{backgroundErr: errors.New("daemon offline")}
	svc := NewToolCallService(repo, nil, router)

	_, err := svc.ConvertToBackground(ctx, connect.NewRequest(&reliantv1.ConvertToBackgroundRequest{
		ToolCallId: toolCallID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	// And the row must not claim BACKGROUNDED for a command that never was.
	calls, listErr := repo.ListToolCallsByIDs(ctx, []string{toolCallID})
	require.NoError(t, listErr)
	if len(calls) > 0 {
		require.NotEqual(t, core.ToolCallStatusBackgrounded, calls[0].Status,
			"a command still running in the foreground must not be recorded as backgrounded")
	}
}
