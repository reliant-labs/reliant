// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A send the client named is saved under the client's id, and the primary key
// is what lets exactly one of several racing repeats create the row.

func countMessagesWithID(t *testing.T, repo *Repo, ctx context.Context, id string) int {
	t.Helper()
	var n int
	require.NoError(t, repo.DB.DB(ctx).QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id = $1`, id).Scan(&n))
	return n
}

func countBlocksOfMessage(t *testing.T, repo *Repo, ctx context.Context, id string) int {
	t.Helper()
	var n int
	require.NoError(t, repo.DB.DB(ctx).QueryRowContext(ctx, `SELECT count(*) FROM message_content_blocks WHERE message_id = $1`, id).Scan(&n))
	return n
}

func TestSaveMessageToThreadOnce_SecondCallWritesNothing(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-once-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)
	id := uuid.NewString()
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)

	msg, created, err := repo.SaveMessageToThreadOnce(ctx, chatID, chatID, user, "hello", nil, nil, nil, id)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, id, msg.ID)

	var updatesBefore int
	require.NoError(t, repo.DB.DB(ctx).QueryRowContext(ctx, `SELECT count(*) FROM chat_updates WHERE chat_id = $1`, chatID).Scan(&updatesBefore))

	msg, created, err = repo.SaveMessageToThreadOnce(ctx, chatID, chatID, user, "hello", nil, nil, nil, id)
	require.NoError(t, err, "a taken id is an outcome, not an error")
	assert.False(t, created)
	assert.Nil(t, msg)

	assert.Equal(t, 1, countMessagesWithID(t, repo, ctx, id))
	assert.Equal(t, 1, countBlocksOfMessage(t, repo, ctx, id), "the repeat must not write the content blocks again")
	var updatesAfter int
	require.NoError(t, repo.DB.DB(ctx).QueryRowContext(ctx, `SELECT count(*) FROM chat_updates WHERE chat_id = $1`, chatID).Scan(&updatesAfter))
	assert.Equal(t, updatesBefore, updatesAfter, "and must not announce the message a second time")
}

func TestSaveMessageToThreadOnce_ConcurrentCallsCreateOneRow(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-once-race-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)
	id := uuid.NewString()
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)

	const writers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	created := make([]bool, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, created[i], errs[i] = repo.SaveMessageToThreadOnce(ctx, chatID, chatID, user, "race", nil, nil, nil, id)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i], "writer %d", i)
		if created[i] {
			winners++
		}
	}
	assert.Equal(t, 1, winners, "exactly one racing writer creates the row")
	assert.Equal(t, 1, countMessagesWithID(t, repo, ctx, id))
	assert.Equal(t, 1, countBlocksOfMessage(t, repo, ctx, id))
}

// Two different messages racing for one chat must still both land: the id
// conflict is the only unique violation the Once path absorbs, and the
// seq/ordinal allocation conflicts between distinct sends still retry.
func TestSaveMessageToThreadOnce_DistinctMessagesAllLand(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-once-distinct-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)

	const writers = 6
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, writers)
	ids := make([]string, writers)
	for i := 0; i < writers; i++ {
		ids[i] = uuid.NewString()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, errs[i] = repo.SaveMessageToThreadOnce(ctx, chatID, chatID, user, "distinct", nil, nil, nil, ids[i])
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i], "writer %d", i)
		assert.Equal(t, 1, countMessagesWithID(t, repo, ctx, ids[i]))
	}
}

func TestSaveMessageToThreadOnce_RequiresAnID(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	_, _, err := repo.SaveMessageToThreadOnce(context.Background(), "c", "c", int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "x", nil, nil, nil, "")
	require.Error(t, err)
}

func TestEnqueueAgentMessageOnce_TakenIDInsertsNothing(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, _ := seedAgentMessageThreads(t, repo, ctx)
	id := uuid.NewString()
	row := func(body string) *AgentMessage {
		return &AgentMessage{
			ID: id, ChatID: chatID, FromThreadID: parentThreadID, ToThreadID: parentThreadID,
			Kind: core.AgentMessageKindHumanMessage, Body: body,
			Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
		}
	}

	inserted, err := repo.EnqueueAgentMessageOnce(ctx, row("first"))
	require.NoError(t, err)
	require.True(t, inserted)

	inserted, err = repo.EnqueueAgentMessageOnce(ctx, row("second"))
	require.NoError(t, err, "a taken id is an outcome, not an error")
	assert.False(t, inserted)

	got, err := repo.GetAgentMessage(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "first", got.Body, "the repeat must not overwrite the row")

	missing, err := repo.GetAgentMessage(ctx, uuid.NewString())
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestEnqueueAgentMessageOnce_ConcurrentCallsInsertOneRow(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, _ := seedAgentMessageThreads(t, repo, ctx)
	id := uuid.NewString()

	const writers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	inserted := make([]bool, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			inserted[i], errs[i] = repo.EnqueueAgentMessageOnce(ctx, &AgentMessage{
				ID: id, ChatID: chatID, FromThreadID: parentThreadID, ToThreadID: parentThreadID,
				Kind: core.AgentMessageKindHumanMessage, Body: "race",
				Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i], "writer %d", i)
		if inserted[i] {
			winners++
		}
	}
	assert.Equal(t, 1, winners)
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	assert.Len(t, queued, 1)
}

func TestDeleteUndeliveredAgentMessage_OnlyTouchesUndeliveredRows(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, _ := seedAgentMessageThreads(t, repo, ctx)
	queuedID, undeliveredID := uuid.NewString(), uuid.NewString()
	enqueueTestAgentMessage(t, repo, ctx, queuedID, chatID, parentThreadID, parentThreadID, "still waiting", time.Now().UTC())
	enqueueTestAgentMessage(t, repo, ctx, undeliveredID, chatID, parentThreadID, parentThreadID, "never read", time.Now().UTC())
	_, err := repo.DB.DB(ctx).ExecContext(ctx, `UPDATE agent_messages SET status = $1 WHERE id = $2`,
		int32(core.AgentMessageStatusUndelivered), undeliveredID)
	require.NoError(t, err)

	deleted, err := repo.DeleteUndeliveredAgentMessage(ctx, queuedID, chatID)
	require.NoError(t, err)
	assert.False(t, deleted, "a row that is still queued is not undelivered")

	deleted, err = repo.DeleteUndeliveredAgentMessage(ctx, undeliveredID, "some-other-chat")
	require.NoError(t, err)
	assert.False(t, deleted, "and a row is only removed from its own chat")

	deleted, err = repo.DeleteUndeliveredAgentMessage(ctx, undeliveredID, chatID)
	require.NoError(t, err)
	assert.True(t, deleted)

	gone, err := repo.GetAgentMessage(ctx, undeliveredID)
	require.NoError(t, err)
	assert.Nil(t, gone)
	kept, err := repo.GetAgentMessage(ctx, queuedID)
	require.NoError(t, err)
	assert.NotNil(t, kept)
}

func TestIsLatestTurnInThread(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-latest-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)
	system := int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)
	assistant := int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	first, err := repo.SaveMessageToThread(ctx, chatID, chatID, user, "question", nil, nil, nil)
	require.NoError(t, err)

	latest, err := repo.IsLatestTurnInThread(ctx, chatID, first.ID)
	require.NoError(t, err)
	assert.True(t, latest, "nothing follows the message")

	_, err = repo.SaveMessageToThread(ctx, chatID, chatID, system, "a note about the run", nil, nil, nil)
	require.NoError(t, err)
	latest, err = repo.IsLatestTurnInThread(ctx, chatID, first.ID)
	require.NoError(t, err)
	assert.True(t, latest, "a system note is not a turn, so it does not make the message look answered")

	_, err = repo.SaveMessageToThread(ctx, chatID, chatID, assistant, "answer", nil, nil, nil)
	require.NoError(t, err)
	latest, err = repo.IsLatestTurnInThread(ctx, chatID, first.ID)
	require.NoError(t, err)
	assert.False(t, latest, "a reply follows it")
}
