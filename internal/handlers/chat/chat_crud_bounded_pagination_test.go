// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// createChatWithMessages seeds a chat with a single (main) thread holding n
// messages, seq/ordinal 1..n, each with one text content block.
func createChatWithMessages(t *testing.T, repo *db.Repo, n int) (ctx context.Context, chatID string) {
	t.Helper()
	ctx = context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID = uuid.New().String()
	threadID := chatID // main thread ID == chat ID when workflow_id is unset, matching MainThreadID()'s fallback in ListMessages
	cwID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "scrollback", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	for i := 1; i <= n; i++ {
		msgID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: msgID, ChatID: chatID, Ordinal: int64(i), Seq: int64(i), ThreadID: threadID,
			ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_USER,
			CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
			ID: uuid.New().String(), MessageID: msgID, Position: 0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   ptr.Of("msg"),
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	return ctx, chatID
}

// TestListMessages_ScrollbackReachesTheBeginning proves the end-to-end
// bounded path (recentLimit > 0) terminates scroll-back at the chat's true
// first message for a chat whose history exceeds one page, and that Total
// and HasMore stay correct across every page.
func TestListMessages_ScrollbackReachesTheBeginning(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	const totalMessages = 25
	const pageSize = 10
	ctx, chatID := createChatWithMessages(t, repo, totalMessages)

	svc := &Service{deps: Deps{Repo: repo}}

	recent := int32(pageSize)
	var beforeSeq *int64
	var seenIDs []string
	pages := 0

	for {
		req := &reliantv1.ListMessagesRequest{ChatId: chatID, Recent: &recent}
		if beforeSeq != nil {
			req.BeforeSeq = beforeSeq
		}
		resp, err := svc.ListMessages(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		pages++
		require.Less(t, pages, 10, "scroll-back did not terminate within a reasonable number of pages")

		require.EqualValues(t, totalMessages, resp.Msg.Total, "Total must always be the true chat-wide count")

		if len(resp.Msg.Messages) == 0 {
			require.False(t, resp.Msg.HasMore, "an empty page must report hasMore=false")
			break
		}

		for _, m := range resp.Msg.Messages {
			seenIDs = append([]string{m.Id}, seenIDs...) // prepend: pages arrive newest-first
		}

		if !resp.Msg.HasMore {
			break
		}
		oldest := resp.Msg.OldestSeq
		beforeSeq = &oldest
	}

	require.Len(t, seenIDs, totalMessages, "scroll-back must eventually surface every message exactly once")
}

// TestListMessages_Bounded_MainThreadWithSiblings proves the bounded path
// merges the inline sibling threads (workflow-node threads) into the main
// thread's window, leaves spawn threads out as the snapshot does, and keeps
// Total the chat-wide count.
func TestListMessages_Bounded_MainThreadWithSiblings(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	mainCWID := uuid.New().String()
	spawnThreadID := uuid.New().String()
	spawnCWID := uuid.New().String()
	nodeThreadID := uuid.New().String()
	nodeCWID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "siblings", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: mainCWID, ThreadID: mainThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{ID: spawnThreadID, ChatID: chatID, ParentThreadID: &mainThreadID, Origin: db.ThreadOriginSpawn, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: spawnCWID, ThreadID: spawnThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{ID: nodeThreadID, ChatID: chatID, ParentThreadID: &mainThreadID, Origin: db.ThreadOriginNode, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: nodeCWID, ThreadID: nodeThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	addMsg := func(threadID, cwID string, seq int64) string {
		msgID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: msgID, ChatID: chatID, Ordinal: seq, Seq: seq, ThreadID: threadID,
			ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_USER,
			CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
			ID: uuid.New().String(), MessageID: msgID, Position: 0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   ptr.Of("msg"),
			CreatedAt: now, UpdatedAt: now,
		}))
		return msgID
	}

	// Interleaved seq: main-1(1), spawn-1(2), node-1(3), main-2(4), spawn-2(5), node-2(6).
	main1 := addMsg(mainThreadID, mainCWID, 1)
	addMsg(spawnThreadID, spawnCWID, 2)
	node1 := addMsg(nodeThreadID, nodeCWID, 3)
	main2 := addMsg(mainThreadID, mainCWID, 4)
	addMsg(spawnThreadID, spawnCWID, 5)
	node2 := addMsg(nodeThreadID, nodeCWID, 6)

	svc := &Service{deps: Deps{Repo: repo}}
	recent := int32(10)
	resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: chatID, Recent: &recent,
	}))
	require.NoError(t, err)

	require.EqualValues(t, 6, resp.Msg.Total, "total is the chat-wide count, spawn messages included")
	gotIDs := make([]string, len(resp.Msg.Messages))
	for i, m := range resp.Msg.Messages {
		gotIDs[i] = m.Id
	}
	require.Equal(t, []string{main1, node1, main2, node2}, gotIDs,
		"main and inline sibling messages, in seq order; spawn-thread messages stay out")

	// A page below a cursor takes the siblings inside its own range only.
	before := int64(4)
	older, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: chatID, Recent: &recent, BeforeSeq: &before,
	}))
	require.NoError(t, err)
	olderIDs := make([]string, len(older.Msg.Messages))
	for i, m := range older.Msg.Messages {
		olderIDs[i] = m.Id
	}
	require.Equal(t, []string{main1, node1}, olderIDs)
	require.False(t, older.Msg.HasMore)
}
