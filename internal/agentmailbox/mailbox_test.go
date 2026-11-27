// Copyright (c) 2025 Reliant Labs
package agentmailbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

func seedThreads(t *testing.T, repo *db.Repo, ctx context.Context) (chatID, parentThreadID, childThreadID string) {
	t.Helper()
	now := time.Now().UTC()
	chatID = uuid.New().String()
	parentThreadID = chatID
	childThreadID = uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "mailbox", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: parentThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID, CreatedAt: now})
	require.NoError(t, err)
	return chatID, parentThreadID, childThreadID
}

// refetches counts the chat-scoped "agent_mailbox" refetches the chat's update
// stream carries.
func refetches(t *testing.T, repo *db.Repo, ctx context.Context, chatID string) int {
	t.Helper()
	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 1000)
	require.NoError(t, err)
	n := 0
	for _, u := range updates {
		if u.UpdateType != db.UpdateTypeRefetch {
			continue
		}
		var data db.RefetchData
		require.NoError(t, json.Unmarshal(u.Data, &data))
		if data.Type == db.RefetchAgentMailbox {
			n++
		}
	}
	return n
}

// Every enqueue announces itself on the chat's update stream, so the pending-
// queue strip re-reads the mailbox on the event instead of polling it every
// few seconds. Enqueue paths that wrote nothing stay silent.
func TestMailbox_AnnouncesEveryEnqueueThatWrote(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	chatID, parentThreadID, childThreadID := seedThreads(t, repo, ctx)
	now := time.Now().UTC()
	ams := store.NewAgentMessageExtStore(repo.ORM())
	mb := New(Deps{Store: ams, Announcer: repo.UpdateWriter()})

	require.Equal(t, 0, refetches(t, repo, ctx, chatID))

	// A peer/human message.
	require.NoError(t, mb.EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindMessage, Body: "hello", Status: core.AgentMessageStatusQueued, CreatedAt: now,
	}))
	assert.Equal(t, 1, refetches(t, repo, ctx, chatID))

	// A repeat of a human send that names its row announces nothing the second time.
	once := &db.AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindHumanMessage, Body: "again", Status: core.AgentMessageStatusQueued, CreatedAt: now,
	}
	inserted, err := mb.EnqueueAgentMessageOnce(ctx, once)
	require.NoError(t, err)
	require.True(t, inserted)
	assert.Equal(t, 2, refetches(t, repo, ctx, chatID))
	inserted, err = mb.EnqueueAgentMessageOnce(ctx, once)
	require.NoError(t, err)
	require.False(t, inserted)
	assert.Equal(t, 2, refetches(t, repo, ctx, chatID))

	// A spawn's terminal report.
	toolCallID := "call-" + uuid.New().String()
	report := &db.AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindCompletion, Body: "done", ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: now,
	}
	outcome, err := mb.EnqueueSpawnReport(ctx, report)
	require.NoError(t, err)
	require.Equal(t, core.SpawnReportInserted, outcome)
	assert.Equal(t, 3, refetches(t, repo, ctx, chatID))

	// The same report again changes nothing, so it announces nothing.
	report.ID = uuid.New().String()
	outcome, err = mb.EnqueueSpawnReport(ctx, report)
	require.NoError(t, err)
	require.Equal(t, core.SpawnReportAlreadyReported, outcome)
	assert.Equal(t, 3, refetches(t, repo, ctx, chatID))

	// Nor does a reconciler placeholder for a spawn that already reported.
	inserted, err = mb.EnqueueAgentMessageIfAbsent(ctx, &db.AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindFailed, Body: "synthesized", ToolCallID: &toolCallID, Synthesized: true,
		Status: core.AgentMessageStatusQueued, CreatedAt: now,
	})
	require.NoError(t, err)
	require.False(t, inserted)
	assert.Equal(t, 3, refetches(t, repo, ctx, chatID))
}

func TestMailbox_RefusesAnIncompleteMessage(t *testing.T) {
	t.Parallel()

	mb := New(Deps{Store: &MockStore{}, Announcer: &MockAnnouncer{}})
	ctx := context.Background()

	assert.Error(t, mb.EnqueueAgentMessage(ctx, nil))
	assert.Error(t, mb.EnqueueAgentMessage(ctx, &db.AgentMessage{ChatID: "c", FromThreadID: "a", ToThreadID: "b"}), "no id")
	assert.Error(t, mb.EnqueueAgentMessage(ctx, &db.AgentMessage{ID: "m", FromThreadID: "a", ToThreadID: "b"}), "no chat")
	_, err := mb.EnqueueAgentMessageIfAbsent(ctx, &db.AgentMessage{ID: "m", ChatID: "c", FromThreadID: "a", ToThreadID: "b", Kind: core.AgentMessageKindMessage})
	assert.Error(t, err, "a placeholder is a terminal kind with a tool call id")
	_, err = mb.EnqueueSpawnReport(ctx, &db.AgentMessage{ID: "m", ChatID: "c", FromThreadID: "a", ToThreadID: "b", Kind: core.AgentMessageKindCompletion})
	assert.Error(t, err, "a report names its tool call")
}
