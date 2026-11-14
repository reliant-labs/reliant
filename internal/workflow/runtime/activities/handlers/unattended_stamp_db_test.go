// Copyright (c) 2025 Reliant Labs
package handlers

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
	"github.com/reliant-labs/reliant/internal/llm/drivers"
)

// An org_only automation's run, and a sub-run its agent started, never reach
// the owner's own account, before or after the trigger is deleted: deleting a
// trigger clears its launch events' trigger_id, which must not turn its runs
// attended (review of #2718, B1). On the migrated schema, through *db.Repo.
func TestUnattendedStamp_DeletedTriggerStaysOrgFundedOnly(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	user, org := "owner-"+uuid.NewString(), "org-"+uuid.NewString()
	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: user, Name: "p", Path: t.TempDir(), OrgID: &org,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))

	config, err := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * 1-5"}, Timezone: "UTC", Overlap: core.ScheduleOverlapSkip})
	require.NoError(t, err)
	triggerID := "trigger-" + uuid.NewString()
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: user, ProjectID: projectID, OrgID: &org, Name: "nightly", Kind: core.TriggerKindSchedule,
		Enabled: true, Workflow: "builtin://agent", Config: config, CreatedAt: now, UpdatedAt: now,
		UnattendedFunding: core.UnattendedFundingOrgOnly,
	}))

	launch := func(chatID string, kind core.TriggerEventKind, trigger *string, payload map[string]any) {
		t.Helper()
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: user, ProjectID: projectID, OrgID: &org,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now}))
		created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
			ID: uuid.NewString(), TriggerID: trigger, UserID: user, Kind: kind, DedupeKey: "fire-" + chatID,
			OccurredAt: now, Payload: payload, Outcome: core.TriggerEventLaunched, ChatID: &chatID, CreatedAt: now,
		})
		require.NoError(t, err)
		require.True(t, created)
	}
	root, child := "chat-"+uuid.NewString(), "chat-"+uuid.NewString()
	launch(root, core.TriggerEventKindSchedule, &triggerID, map[string]any{})
	launch(child, core.TriggerEventKindAgentStartRun, nil, map[string]any{"parent_chat_id": root})

	personalOnly := func(t *testing.T, chatID string) {
		t.Helper()
		chat, err := repo.GetChat(ctx, chatID)
		require.NoError(t, err)
		who := chatAttendance(ctx, repo, chat)
		assert.False(t, who.attended, "%s is unattended", chatID)
		assert.False(t, who.anySource, "%s spends the org's own sources only", chatID)
		scope := llmCallSpec{UserID: user, OrgID: org, Attended: who.attended, UnattendedAnySource: who.anySource}.scope()
		require.True(t, scope.OrgFundedOnly())
		avail, err := drivers.BuildAvailableDrivers(ctx, personalOnlyStore{}, scope)
		require.NoError(t, err)
		assert.Empty(t, avail.Accounts["anthropic"], "%s never reaches the owner's own account", chatID)
	}

	t.Run("while the trigger exists", func(t *testing.T) {
		personalOnly(t, root)
		personalOnly(t, child)
	})

	require.NoError(t, repo.DeleteTrigger(ctx, triggerID))
	chat, err := repo.GetChat(ctx, root)
	require.NoError(t, err)
	require.Nil(t, chat.TriggerID, "deleting the trigger cleared the launch's trigger id")
	require.Equal(t, string(core.TriggerEventKindSchedule), chat.LaunchKind, "the launch keeps its kind")

	t.Run("after the trigger is deleted", func(t *testing.T) {
		personalOnly(t, root)
		personalOnly(t, child)
	})
}
