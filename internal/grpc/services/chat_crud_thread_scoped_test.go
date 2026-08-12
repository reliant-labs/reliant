// Copyright (c) 2025 Reliant Labs
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
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// threadScopedFixture is a chat with a main thread and one spawn thread whose
// messages interleave by seq -- the shape that made the chat-wide read
// unreliable for a single-thread consumer. Spawn messages sit ABOVE the main
// thread's newest message, which is exactly how a real spawn behaves: it
// out-writes and out-lives the thread that started it.
type threadScopedFixture struct {
	chatID       string
	mainThreadID string
	spawnThread  string
	mainMsgIDs   []string
	spawnMsgIDs  []string
}

func seedThreadScopedChat(t *testing.T, repo *db.Repo, ctx context.Context, mainCount, spawnCount int) threadScopedFixture {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	mainCWID := uuid.New().String()
	spawnThreadID := uuid.New().String()
	spawnCWID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "thread-scoped", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: mainCWID, ThreadID: mainThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: spawnThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: spawnCWID, ThreadID: spawnThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	var seq int64
	addMsg := func(threadID, cwID string) string {
		seq++
		msgID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: msgID, ChatID: chatID, Ordinal: seq, Seq: seq, ThreadID: threadID,
			ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
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

	f := threadScopedFixture{chatID: chatID, mainThreadID: mainThreadID, spawnThread: spawnThreadID}
	// Main thread writes first, then the spawn runs long past it.
	for i := 0; i < mainCount; i++ {
		f.mainMsgIDs = append(f.mainMsgIDs, addMsg(mainThreadID, mainCWID))
	}
	for i := 0; i < spawnCount; i++ {
		f.spawnMsgIDs = append(f.spawnMsgIDs, addMsg(spawnThreadID, spawnCWID))
	}
	return f
}

func listThreadMessagesReq(chatID, threadID string) *connect.Request[reliantv1.ListMessagesRequest] {
	return connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: chatID, ThreadId: &threadID,
	})
}

// TestListMessages_ThreadScoped_ReturnsOnlyThatThread is the core guarantee:
// asking for a thread returns that thread's messages and no others, whatever
// the rest of the chat looks like.
func TestListMessages_ThreadScoped_ReturnsOnlyThatThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	f := seedThreadScopedChat(t, repo, ctx, 3, 5)
	svc := &ChatService{database: repo}

	resp, err := svc.ListMessages(ctx, listThreadMessagesReq(f.chatID, f.spawnThread))
	require.NoError(t, err)

	gotIDs := make([]string, len(resp.Msg.Messages))
	for i, m := range resp.Msg.Messages {
		gotIDs[i] = m.Id
	}
	require.ElementsMatch(t, f.spawnMsgIDs, gotIDs,
		"a thread-scoped read must return exactly that thread's messages")
	require.EqualValues(t, len(f.spawnMsgIDs), resp.Msg.Total)
}

// TestListMessages_ThreadScoped_UnaffectedByMainThreadVolume is the bug this
// whole change exists to kill. A spawn that has out-written the main thread by
// an order of magnitude used to fall outside a window sized for the main
// transcript, and its preview rendered "Starting..." over a thread with
// hundreds of messages. A thread-scoped read cannot have that failure mode:
// there is no window sized for somebody else.
func TestListMessages_ThreadScoped_UnaffectedByMainThreadVolume(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	// 2 main-thread messages, 250 spawn messages -- past any page size the
	// chat-wide read would have applied.
	f := seedThreadScopedChat(t, repo, ctx, 2, 250)
	svc := &ChatService{database: repo}

	resp, err := svc.ListMessages(ctx, listThreadMessagesReq(f.chatID, f.spawnThread))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Messages, 250,
		"every message of the requested thread must be returned regardless of the chat's shape")
	require.False(t, resp.Msg.HasMore, "an unbounded thread read has nothing older")
}

// TestListMessages_ThreadScoped_Bounded checks the cursor form: `recent`
// bounds the page and has_more reports honestly.
func TestListMessages_ThreadScoped_Bounded(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	f := seedThreadScopedChat(t, repo, ctx, 1, 10)
	svc := &ChatService{database: repo}

	recent := int32(4)
	resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: f.chatID, ThreadId: &f.spawnThread, Recent: &recent,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Messages, 4, "recent bounds a thread-scoped page")
	require.True(t, resp.Msg.HasMore, "6 older messages remain on this thread")
	require.EqualValues(t, 10, resp.Msg.Total, "total counts the whole thread, not the page")

	// The page must be the NEWEST 4 of the thread.
	require.Equal(t, f.spawnMsgIDs[len(f.spawnMsgIDs)-4:], idsOf(resp.Msg.Messages))
}

// TestListMessages_ThreadScoped_UnknownThread proves an unresolvable thread is
// an error, not a quiet fall back to the chat-wide list. Answering a different
// question than the one asked is the failure mode this change removes.
func TestListMessages_ThreadScoped_UnknownThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	f := seedThreadScopedChat(t, repo, ctx, 2, 2)
	svc := &ChatService{database: repo}

	_, err := svc.ListMessages(ctx, listThreadMessagesReq(f.chatID, uuid.New().String()))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestListMessages_ThreadScoped_ThreadFromAnotherChat proves the thread must
// belong to the authorized chat -- a thread id alone is not an access grant.
func TestListMessages_ThreadScoped_ThreadFromAnotherChat(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	a := seedThreadScopedChat(t, repo, ctx, 2, 2)
	b := seedThreadScopedChat(t, repo, ctx, 2, 2)
	svc := &ChatService{database: repo}

	_, err := svc.ListMessages(ctx, listThreadMessagesReq(a.chatID, b.spawnThread))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestListMessages_ThreadIDUnset_MatchesChatWideBehavior pins the additive
// property: the field is opt-in, and omitting it changes nothing.
func TestListMessages_ThreadIDUnset_MatchesChatWideBehavior(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	f := seedThreadScopedChat(t, repo, ctx, 3, 4)
	svc := &ChatService{database: repo}

	recent := int32(50)
	resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: f.chatID, Recent: &recent,
	}))
	require.NoError(t, err)

	want := append(append([]string{}, f.mainMsgIDs...), f.spawnMsgIDs...)
	require.ElementsMatch(t, want, idsOf(resp.Msg.Messages),
		"omitting thread_id must still return the whole chat")
}

func idsOf(msgs []*reliantv1.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.Id
	}
	return ids
}
