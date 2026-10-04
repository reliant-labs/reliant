// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// agent.start_run is a trigger-event kind of its own. The kind CHECK must admit
// it — before the migration that widened it, this insert is rejected by the
// database.
func TestTriggerEvent_AgentStartRunKindIsAccepted(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := uuid.NewString()
	chatWithRootWorkflow(t, repo, chatID, chatID, Active())

	created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: "test-user", Kind: core.TriggerEventKindAgentStartRun,
		DedupeKey: "parent:toolu_1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"parent_chat_id": "parent"},
		Outcome: core.TriggerEventLaunched, ChatID: &chatID,
	})
	require.NoError(t, err)
	assert.True(t, created)

	// Unique within the kind: the same dedupe key is the same launch.
	created, err = repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: "test-user", Kind: core.TriggerEventKindAgentStartRun,
		DedupeKey: "parent:toolu_1", OccurredAt: time.Now().UTC(),
		Outcome: core.TriggerEventLaunched, ChatID: &chatID,
	})
	require.NoError(t, err)
	assert.False(t, created, "a retried start must not write a second event")

	// An unknown kind is still refused: widening the CHECK added one value,
	// it did not remove the guard.
	_, err = repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: "test-user", Kind: core.TriggerEventKind("agent.bogus"),
		DedupeKey: "x", OccurredAt: time.Now().UTC(), Outcome: core.TriggerEventLaunched,
	})
	require.Error(t, err)
}

func TestGetTriggerEventByChat_FindsTheLaunchingEventOfThatKind(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := uuid.NewString()
	chatWithRootWorkflow(t, repo, chatID, chatID, Active())
	_, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: "test-user", Kind: core.TriggerEventKindAgentStartRun,
		DedupeKey: "p:t", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"parent_chat_id": "p"}, Outcome: core.TriggerEventLaunched, ChatID: &chatID,
	})
	require.NoError(t, err)

	got, err := repo.GetTriggerEventByChat(ctx, core.TriggerEventKindAgentStartRun, chatID)
	require.NoError(t, err)
	assert.Equal(t, "p", got.Payload["parent_chat_id"])

	// A different kind did not launch this chat.
	_, err = repo.GetTriggerEventByChat(ctx, core.TriggerEventKindChatStart, chatID)
	assert.ErrorIs(t, err, core.ErrTriggerEventNotFound)
	// Nor did anything launch a chat that does not exist.
	_, err = repo.GetTriggerEventByChat(ctx, core.TriggerEventKindAgentStartRun, uuid.NewString())
	assert.ErrorIs(t, err, core.ErrTriggerEventNotFound)
}

func TestCountLiveLaunchedRuns_CountsOnlyLiveRunsOfThatKindAndUser(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	launch := func(user string, kind core.TriggerEventKind, status WorkflowStatus) {
		t.Helper()
		id := uuid.NewString()
		chatWithRootWorkflow(t, repo, id, id, status)
		if user != "test-user" {
			chat, err := repo.GetChat(ctx, id)
			require.NoError(t, err)
			chat.UserID = user
			require.NoError(t, repo.UpdateChat(ctx, chat))
		}
		_, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
			ID: uuid.NewString(), UserID: user, Kind: kind, DedupeKey: id,
			OccurredAt: time.Now().UTC(), Outcome: core.TriggerEventLaunched, ChatID: &id,
		})
		require.NoError(t, err)
	}

	kind := core.TriggerEventKindAgentStartRun
	launch("test-user", kind, Pending())
	launch("test-user", kind, Active())
	launch("test-user", kind, Paused())
	// Not live:
	launch("test-user", kind, Completed())
	launch("test-user", kind, Failed())
	launch("test-user", kind, Cancelled())
	// Live but another kind, and live but another user:
	launch("test-user", core.TriggerEventKindChatStart, Active())
	launch("other-user", kind, Active())

	count, err := repo.CountLiveLaunchedRuns(ctx, "test-user", kind)
	require.NoError(t, err)
	assert.Equal(t, 3, count, "pending + running + paused of this user's agent-started runs")

	count, err = repo.CountLiveLaunchedRuns(ctx, "nobody", kind)
	require.NoError(t, err)
	assert.Zero(t, count)
}

// ListChats with no project filter must mean "every project", not "no rows":
// the project predicate used to compare against NULL, which matches nothing, so
// listing a user's chats across projects returned an empty list.
func TestListChats_NilProjectMeansEveryProject(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	for _, project := range []string{"project-a", "project-b"} {
		id := uuid.NewString()
		now := time.Now()
		require.NoError(t, repo.CreateChat(ctx, &Chat{
			ID: id, Title: project, ProjectID: project, UserID: "list-user",
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}

	all, err := repo.ListChats(ctx, ChatFilters{UserID: "list-user", Limit: 50})
	require.NoError(t, err)
	assert.Len(t, all, 2, "no project filter lists every project")

	projectA := "project-a"
	one, err := repo.ListChats(ctx, ChatFilters{UserID: "list-user", ProjectID: &projectA, Limit: 50})
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, "project-a", one[0].ProjectID)
}
