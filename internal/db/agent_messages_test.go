// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// seedAgentMessageThreads creates a chat with a parent and a child thread --
// the shape a spawn_send / completion-notification write always has -- and
// returns their ids.
func seedAgentMessageThreads(t *testing.T, repo *Repo, ctx context.Context) (chatID, parentThreadID, childThreadID string) {
	t.Helper()
	now := time.Now().UTC()

	chatID = uuid.New().String()
	parentThreadID = chatID // root thread id equals the chat id (see createTestRootThread)
	childThreadID = uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "mailbox", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: parentThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &Thread{ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID, CreatedAt: now})
	require.NoError(t, err)

	return chatID, parentThreadID, childThreadID
}

// seedDeliveredMessage creates a real message row on the given thread, for
// use as an agent_messages.delivered_message_id target.
func seedDeliveredMessage(t *testing.T, repo *Repo, ctx context.Context, chatID, threadID string) string {
	t.Helper()
	now := time.Now().UTC()

	cwID := uuid.New().String()
	messageID := uuid.New().String()
	_, err := repo.CreateContextWindow(ctx, &ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt: now, UpdatedAt: now,
	}))
	return messageID
}

func enqueueTestAgentMessage(t *testing.T, repo *Repo, ctx context.Context, id, chatID, fromThreadID, toThreadID, body string, createdAt time.Time) {
	t.Helper()
	require.NoError(t, repo.EnqueueAgentMessage(ctx, &AgentMessage{
		ID: id, ChatID: chatID, FromThreadID: fromThreadID, ToThreadID: toThreadID,
		Kind: core.AgentMessageKindMessage, Body: body,
		Status: core.AgentMessageStatusQueued, CreatedAt: createdAt,
	}))
}

// TestListQueuedAgentMessagesForThread_OrderedByCreatedAt pins the ordering
// invariant the spec calls load-bearing: messages must be delivered in the
// order they were sent, not the order rows happen to be inserted.
func TestListQueuedAgentMessagesForThread_OrderedByCreatedAt(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// Enqueued out of chronological order to prove the read, not the write,
	// establishes the ordering.
	enqueueTestAgentMessage(t, repo, ctx, "m-second", chatID, parentThreadID, childThreadID, "second", base.Add(10*time.Minute))
	enqueueTestAgentMessage(t, repo, ctx, "m-first", chatID, parentThreadID, childThreadID, "first", base)
	enqueueTestAgentMessage(t, repo, ctx, "m-third", chatID, parentThreadID, childThreadID, "third", base.Add(20*time.Minute))

	// A message queued for a different thread must not leak in.
	enqueueTestAgentMessage(t, repo, ctx, "m-other", chatID, childThreadID, parentThreadID, "not for the child", base)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 3)
	require.Equal(t, []string{"m-first", "m-second", "m-third"}, []string{queued[0].ID, queued[1].ID, queued[2].ID})
}

// TestMarkAgentMessagesDelivered_RemovesFromQueueAndRecordsDelivery covers
// the drain path: marking a batch delivered must stop it from appearing in
// the queued list and must populate delivered_at + delivered_message_id.
func TestMarkAgentMessagesDelivered_RemovesFromQueueAndRecordsDelivery(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	enqueueTestAgentMessage(t, repo, ctx, "m-1", chatID, parentThreadID, childThreadID, "one", base)
	enqueueTestAgentMessage(t, repo, ctx, "m-2", chatID, parentThreadID, childThreadID, "two", base.Add(time.Minute))
	enqueueTestAgentMessage(t, repo, ctx, "m-3", chatID, parentThreadID, childThreadID, "three", base.Add(2*time.Minute))

	deliveredMessageID := seedDeliveredMessage(t, repo, ctx, chatID, childThreadID)
	deliveredAt := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.MarkAgentMessagesDelivered(ctx, []string{"m-1", "m-2"}, deliveredAt, deliveredMessageID))

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "delivered messages must drop out of the queue")
	require.Equal(t, "m-3", queued[0].ID)

	count, err := repo.CountQueuedAgentMessagesForThread(ctx, childThreadID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

// TestAgentMessages_DeliveredHasTimeConstraint is a schema-level assertion:
// the CHECK constraint must reject a row claiming to be delivered (status=2)
// without a delivered_at, the same contradiction tool_calls' analogous
// constraint refuses to store.
func TestAgentMessages_DeliveredHasTimeConstraint(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	now := time.Now().UTC()

	_, err := rawDB.ExecContext(ctx, `
		INSERT INTO agent_messages (id, chat_id, from_thread_id, to_thread_id, kind, body, status, created_at, delivered_at)
		VALUES ($1, $2, $3, $4, 1, 'no delivery time', 2, $5, NULL)
	`, uuid.New().String(), chatID, parentThreadID, childThreadID, now)

	require.Error(t, err, "a status=2 row with no delivered_at must be rejected by the CHECK constraint")
	require.Contains(t, err.Error(), "agent_messages_delivered_has_time")
}

// TestCountQueuedAgentMessagesForThread_OnlyCountsQueued asserts the count
// reflects status=1 rows only -- delivered history must not inflate it.
func TestCountQueuedAgentMessagesForThread_OnlyCountsQueued(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	count, err := repo.CountQueuedAgentMessagesForThread(ctx, childThreadID)
	require.NoError(t, err)
	require.Equal(t, int64(0), count, "an empty inbox counts zero")

	enqueueTestAgentMessage(t, repo, ctx, "m-a", chatID, parentThreadID, childThreadID, "a", base)
	enqueueTestAgentMessage(t, repo, ctx, "m-b", chatID, parentThreadID, childThreadID, "b", base.Add(time.Minute))

	count, err = repo.CountQueuedAgentMessagesForThread(ctx, childThreadID)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	deliveredMessageID := seedDeliveredMessage(t, repo, ctx, chatID, childThreadID)
	require.NoError(t, repo.MarkAgentMessagesDelivered(ctx, []string{"m-a"}, time.Now().UTC(), deliveredMessageID))

	count, err = repo.CountQueuedAgentMessagesForThread(ctx, childThreadID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "a delivered row must not be counted as queued")
}
