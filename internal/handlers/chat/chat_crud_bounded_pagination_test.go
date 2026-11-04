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

// siblingChat seeds a chat with a main thread and one inline node thread, and
// returns a function that adds a message to either at a given seq.
func siblingChat(t *testing.T, repo *db.Repo) (ctx context.Context, chatID string, add func(onMain bool, seq int64) string) {
	t.Helper()
	ctx = context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	chatID = uuid.New().String()
	mainThreadID, mainCWID := chatID, uuid.New().String()
	nodeThreadID, nodeCWID := uuid.New().String(), uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, Title: "siblings", ProjectID: "test-project", UserID: "test-user", CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: mainCWID, ThreadID: mainThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{ID: nodeThreadID, ChatID: chatID, ParentThreadID: &mainThreadID, Origin: db.ThreadOriginNode, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: nodeCWID, ThreadID: nodeThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	add = func(onMain bool, seq int64) string {
		threadID, cwID := nodeThreadID, nodeCWID
		if onMain {
			threadID, cwID = mainThreadID, mainCWID
		}
		msgID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: msgID, ChatID: chatID, Ordinal: seq, Seq: seq, ThreadID: threadID,
			ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
			ID: uuid.New().String(), MessageID: msgID, Position: 0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("msg"), CreatedAt: now, UpdatedAt: now,
		}))
		return msgID
	}
	return ctx, chatID, add
}

// scrollBack pages ListMessages to the beginning and returns every id seen,
// oldest first.
func scrollBack(t *testing.T, svc *Service, ctx context.Context, chatID string, pageSize int32) []string {
	t.Helper()
	var beforeSeq *int64
	var seen []string
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20, "scroll-back did not terminate")
		resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: chatID, Recent: &pageSize, BeforeSeq: beforeSeq}))
		require.NoError(t, err)
		page := make([]string, 0, len(resp.Msg.Messages))
		for _, m := range resp.Msg.Messages {
			page = append(page, m.Id)
		}
		seen = append(page, seen...)
		if !resp.Msg.HasMore || len(page) == 0 {
			return seen
		}
		oldest := resp.Msg.OldestSeq
		beforeSeq = &oldest
	}
}

// A sibling message older than the main thread's first message is served on
// the oldest page, even when that page holds exactly a page of main messages.
func TestListMessages_Bounded_SiblingOlderThanMainThread(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx, chatID, add := siblingChat(t, repo)
	want := []string{add(false, 1), add(false, 2), add(true, 3), add(true, 4), add(true, 5)}
	got := scrollBack(t, &Service{deps: Deps{Repo: repo}}, ctx, chatID, 3)
	require.Equal(t, want, got)
}

// A chat whose main thread is empty pages its siblings a page at a time
// instead of reading them all at once, and still reaches the beginning.
func TestListMessages_Bounded_EmptyMainThreadPagesSiblings(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx, chatID, add := siblingChat(t, repo)
	var want []string
	for seq := int64(1); seq <= 7; seq++ {
		want = append(want, add(false, seq))
	}
	svc := &Service{deps: Deps{Repo: repo}}
	recent := int32(3)
	first, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: chatID, Recent: &recent}))
	require.NoError(t, err)
	require.Len(t, first.Msg.Messages, 3, "one page of siblings, not all of them")
	require.True(t, first.Msg.HasMore)
	require.Equal(t, want, scrollBack(t, svc, ctx, chatID, 3))
}
