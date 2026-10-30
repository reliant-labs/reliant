// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The chat_origin migration backfills every existing chat's origin from its
// launch event (the earliest trigger_events row), with the mapping the
// launcher uses for new chats (core.ChatOriginOf), and the sidebar policy
// reads the column from then on. The migration file itself runs over seeded
// rows, so what ships is what is tested.
func TestChatOriginMigrationBackfillsFromLaunchEvent(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := raw.Exec(q, args...)
		require.NoError(t, err, q)
	}

	triggerID := "trg-backfill"
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(triggerID, "test-user", "test-project", "pr-review")))
	now := time.Now().UTC()

	kinds := []core.TriggerEventKind{
		core.TriggerEventKindChatStart,
		core.TriggerEventKindSchedule,
		core.TriggerEventKindWebhook,
		core.TriggerEventKindIntegration,
		core.TriggerEventKindWorkflowEvent,
		core.TriggerEventKindAgentStartRun,
		core.TriggerEventKindManualRun,
		core.TriggerEventKindBuilderTest,
	}
	for _, kind := range kinds {
		id := "bf-" + string(kind)
		createActivityTestChat(t, repo, id)
		var trigger *string
		if kind.Unattended() {
			trigger = &triggerID
		}
		recordLaunchEvent(t, repo, id, kind, trigger, now)
	}
	createActivityTestChat(t, repo, "bf-legacy") // no launch event: predates triggers
	// A run whose launch event is a schedule, later continued with chat.start:
	// the earliest event decides.
	createActivityTestChat(t, repo, "bf-continued")
	recordLaunchEvent(t, repo, "bf-continued", core.TriggerEventKindSchedule, &triggerID, now.Add(-time.Hour))
	recordLaunchEvent(t, repo, "bf-continued", core.TriggerEventKindChatStart, nil, now)
	// An adopted automation run stays listed after the backfill.
	createActivityTestChat(t, repo, "bf-adopted")
	recordLaunchEvent(t, repo, "bf-adopted", core.TriggerEventKindIntegration, &triggerID, now)
	_, err := repo.SetChatAdopted(ctx, "bf-adopted", "test-user", true)
	require.NoError(t, err)

	// Put the database back where it was before the migration: no origin
	// column (dropping it drops the view that reads it). Every row then reads
	// as it did on the previous release.
	exec(`ALTER TABLE chats DROP COLUMN origin CASCADE`)

	// Found by name, not version: the merge queue re-versions a branch's
	// migrations above main's when it lands.
	matches, err := fs.Glob(forgedb.MigrationsFS, "migrations/*_chat_origin.up.sql")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	body, err := forgedb.MigrationsFS.ReadFile(matches[0])
	require.NoError(t, err)
	exec(string(body))

	for _, kind := range kinds {
		id := "bf-" + string(kind)
		chat, err := repo.GetChat(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, core.ChatOriginOf(kind), chat.Origin, "%s: the backfill and the launcher agree", id)
		assert.Equal(t, kind == core.TriggerEventKindChatStart, chat.ListInSidebar, "%s: only a person's chat lists", id)
	}
	legacy, err := repo.GetChat(ctx, "bf-legacy")
	require.NoError(t, err)
	assert.Equal(t, core.ChatOriginUser, legacy.Origin)
	assert.True(t, legacy.ListInSidebar, "a chat with no launch event is a conversation")

	continued, err := repo.GetChat(ctx, "bf-continued")
	require.NoError(t, err)
	assert.Equal(t, core.ChatOriginAutomation, continued.Origin, "the earliest event is the launch event")
	assert.False(t, continued.ListInSidebar)

	adopted, err := repo.GetChat(ctx, "bf-adopted")
	require.NoError(t, err)
	assert.Equal(t, core.ChatOriginAutomation, adopted.Origin, "adoption never rewrites origin")
	assert.True(t, adopted.ListInSidebar)

	// The previous release inserts chats without naming the column: the
	// default keeps that insert working.
	exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		VALUES ('bf-old-release', 't', 'test-project', 'test-user', now(), now(), now())`)
	old, err := repo.GetChat(ctx, "bf-old-release")
	require.NoError(t, err)
	assert.Equal(t, core.ChatOriginUser, old.Origin)
}
