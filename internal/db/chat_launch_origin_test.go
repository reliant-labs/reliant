// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// chats_with_activity carries the chat's origin — launch_kind and trigger_id
// from its earliest trigger_events row — so the UI can tell automation chats
// from interactive ones without a lookup per chat.

func recordLaunchEvent(t *testing.T, repo *Repo, chatID string, kind core.TriggerEventKind, triggerID *string, at time.Time) {
	t.Helper()
	created, err := repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID:         "evt-" + chatID + "-" + string(kind),
		TriggerID:  triggerID,
		UserID:     "test-user",
		Kind:       kind,
		DedupeKey:  "dedupe-" + chatID + "-" + string(kind),
		OccurredAt: at,
		Payload:    map[string]any{"trigger_name": "nightly"},
		Outcome:    core.TriggerEventLaunched,
		ChatID:     &chatID,
		CreatedAt:  at,
	})
	require.NoError(t, err)
	require.True(t, created)
}

func TestChatReadsCarryLaunchOrigin(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	triggerID := "trg-origin"
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(triggerID, "test-user", "test-project", "nightly")))

	createActivityTestChat(t, repo, "origin-schedule")
	recordLaunchEvent(t, repo, "origin-schedule", core.TriggerEventKindSchedule, &triggerID, time.Now().UTC())

	createActivityTestChat(t, repo, "origin-interactive")
	recordLaunchEvent(t, repo, "origin-interactive", core.TriggerEventKindChatStart, nil, time.Now().UTC())

	createActivityTestChat(t, repo, "origin-legacy") // no event: predates triggers

	t.Run("GetChat schedule", func(t *testing.T) {
		chat, err := repo.GetChat(ctx, "origin-schedule")
		require.NoError(t, err)
		assert.Equal(t, "schedule", chat.LaunchKind)
		require.NotNil(t, chat.TriggerID)
		assert.Equal(t, triggerID, *chat.TriggerID)
	})
	t.Run("GetChatWithUserCheck interactive", func(t *testing.T) {
		chat, err := repo.GetChatWithUserCheck(ctx, "origin-interactive", "test-user")
		require.NoError(t, err)
		assert.Equal(t, "chat.start", chat.LaunchKind)
		assert.Nil(t, chat.TriggerID)
	})
	t.Run("legacy chat has no origin", func(t *testing.T) {
		chat, err := repo.GetChat(ctx, "origin-legacy")
		require.NoError(t, err)
		assert.Empty(t, chat.LaunchKind)
		assert.Nil(t, chat.TriggerID)
	})
	t.Run("ListChats", func(t *testing.T) {
		project := "test-project"
		chats, err := repo.ListChats(ctx, ChatFilters{UserID: "test-user", ProjectID: &project, Limit: 100})
		require.NoError(t, err)
		kinds := map[string]string{}
		for _, c := range chats {
			kinds[c.ID] = c.LaunchKind
		}
		assert.Equal(t, "schedule", kinds["origin-schedule"])
		assert.Equal(t, "chat.start", kinds["origin-interactive"])
		assert.Equal(t, "", kinds["origin-legacy"])
	})
}

func TestChatLaunchOriginUsesEarliestEventAndSurvivesTriggerDeletion(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	triggerID := "trg-delete"
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(triggerID, "test-user", "test-project", "doomed")))
	createActivityTestChat(t, repo, "origin-two-events")
	now := time.Now().UTC()
	recordLaunchEvent(t, repo, "origin-two-events", core.TriggerEventKindSchedule, &triggerID, now.Add(-time.Hour))
	recordLaunchEvent(t, repo, "origin-two-events", core.TriggerEventKindChatStart, nil, now)

	chat, err := repo.GetChat(ctx, "origin-two-events")
	require.NoError(t, err)
	assert.Equal(t, "schedule", chat.LaunchKind, "the earliest event is the launch event")

	require.NoError(t, repo.DeleteTrigger(ctx, triggerID))
	chat, err = repo.GetChat(ctx, "origin-two-events")
	require.NoError(t, err)
	assert.Equal(t, "schedule", chat.LaunchKind, "deleting the trigger keeps the origin kind")
	assert.Nil(t, chat.TriggerID, "ON DELETE SET NULL clears the trigger id")

	ev, err := repo.GetTriggerEventByChatID(ctx, "origin-two-events")
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventKindSchedule, ev.Kind)
	_, err = repo.GetTriggerEventByChatID(ctx, "no-chat")
	assert.ErrorIs(t, err, core.ErrTriggerEventNotFound)
}

func TestListChatsExcludeAutomations(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	triggerID := "trg-exclude"
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(triggerID, "test-user", "test-project", "nightly")))
	now := time.Now().UTC()

	createActivityTestChat(t, repo, "x-interactive")
	recordLaunchEvent(t, repo, "x-interactive", core.TriggerEventKindChatStart, nil, now)
	createActivityTestChat(t, repo, "x-legacy")
	createActivityTestChat(t, repo, "x-schedule-quiet")
	recordLaunchEvent(t, repo, "x-schedule-quiet", core.TriggerEventKindSchedule, &triggerID, now)
	createActivityTestChat(t, repo, "x-schedule-needs-approval")
	recordLaunchEvent(t, repo, "x-schedule-needs-approval", core.TriggerEventKindSchedule, &triggerID, now)
	insertTestApproval(t, repo, "x-approval", "x-schedule-needs-approval", 1) // pending

	list := func(excludeAutomations bool) map[string]bool {
		project := "test-project"
		chats, err := repo.ListChats(ctx, ChatFilters{
			UserID: "test-user", ProjectID: &project, Limit: 100,
			SidebarOnly: excludeAutomations,
		})
		require.NoError(t, err)
		ids := map[string]bool{}
		for _, c := range chats {
			ids[c.ID] = true
		}
		return ids
	}

	all := list(false)
	for _, id := range []string{"x-interactive", "x-legacy", "x-schedule-quiet", "x-schedule-needs-approval"} {
		assert.True(t, all[id], "without the filter %s is listed", id)
	}

	filtered := list(true)
	assert.True(t, filtered["x-interactive"])
	assert.True(t, filtered["x-legacy"], "a chat with no launch event is interactive")
	assert.False(t, filtered["x-schedule-quiet"], "a quiet scheduled chat is hidden")
	assert.True(t, filtered["x-schedule-needs-approval"], "an automation chat awaiting input is kept")
}
