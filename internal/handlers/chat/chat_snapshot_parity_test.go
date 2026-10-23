// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/streaming"
	"github.com/reliant-labs/reliant/internal/ptr"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// This file compares ChatService's reload path with the streaming service's
// live path, so it builds both and moves with whichever lane goes second
// (research/forge-migration/06-p3b-recipe.md). The streaming half's other
// snapshot tests live with it in internal/handlers/streaming.

// TestChatSnapshot_MatchesListMessagesEnrichment states the invariant the
// shared assembler exists to hold: the live path and the reload path describe
// the same tool call the same way. A difference here is a user-visible
// inconsistency -- the screen changes when you reload it.
func TestChatSnapshot_MatchesListMessagesEnrichment(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	cwID := uuid.New().String()
	childThreadID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()[:8]

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "parity", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: mainThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	msgID := uuid.New().String()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msgID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: mainThreadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(toolCallID),
		ToolName:   ptr.Of("spawn"),
		ToolInput:  ptr.Of(`{"title":"child agent"}`),
		CreatedAt:  now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &mainThreadID, MessageID: &msgID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &childThreadID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	findSpawnBlock := func(msgs []*reliantv1.Message) *reliantv1.ContentBlock {
		for _, m := range msgs {
			for _, b := range m.ContentBlocks {
				if b.ToolCallId != nil && *b.ToolCallId == toolCallID {
					return b
				}
			}
		}
		return nil
	}

	fromSnapshot := findSpawnBlock(chatSnapshotOverStream(t, repo, "test-user", chatID).Messages)
	require.NotNil(t, fromSnapshot)

	recent := int32(50)
	resp, err := (&Service{deps: Deps{Repo: repo}}).ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: chatID, Recent: &recent,
	}))
	require.NoError(t, err)
	fromList := findSpawnBlock(resp.Msg.Messages)
	require.NotNil(t, fromList)

	require.Equal(t, fromList.ChildWorkflowId, fromSnapshot.ChildWorkflowId,
		"live and reload paths must agree on which thread the spawn owns")
	require.Equal(t, fromList.ToolCallStatus, fromSnapshot.ToolCallStatus,
		"live and reload paths must agree on tool-call status")
}

// chatSnapshotOverStream subscribes userID to chatID and returns the first
// chat snapshot the streaming service sends: the snapshot a client receives,
// read the way the client reads it.
func chatSnapshotOverStream(t *testing.T, repo streaming.Repository, userID, chatID string) *reliantv1.ChatSyncSnapshot {
	t.Helper()

	svc, err := streaming.New(streaming.Deps{
		Logger: slog.Default(),
		Config: &typedconfig.Config{},
		Repo:   repo,
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc)
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.UserIDContextKey, userID)))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL).
		StreamUserUpdates(ctx, connect.NewRequest(&reliantv1.StreamUserUpdatesRequest{SubscribeChatId: &chatID}))
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	for stream.Receive() {
		if snapshot := stream.Msg().GetChatSyncSnapshot(); snapshot != nil {
			return snapshot
		}
	}
	t.Fatalf("stream ended before a chat snapshot: %v", stream.Err())
	return nil
}
