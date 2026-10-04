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

// builder.test is a trigger-event kind of its own (WORKFLOW_UI.md §13 G6). The
// kind CHECK must admit it.
func TestTriggerEvent_BuilderTestKindIsAccepted(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := uuid.NewString()
	chatWithRootWorkflow(t, repo, chatID, chatID, Active())

	created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: "test-user", Kind: core.TriggerEventKindBuilderTest,
		DedupeKey: chatID, OccurredAt: time.Now().UTC(),
		Outcome: core.TriggerEventLaunched, ChatID: &chatID,
	})
	require.NoError(t, err)
	assert.True(t, created)

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "builder.test", chat.LaunchKind)
}

// A builder test run is never in the sidebar, adopted or not awaiting input
// notwithstanding: it is scratch work, and it is in the Runs "Tests" view.
func TestBuilderTestRunIsNotListedInSidebar(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	createActivityTestChat(t, repo, "bt-run")
	recordLaunchEvent(t, repo, "bt-run", core.TriggerEventKindBuilderTest, nil, now)
	createActivityTestChat(t, repo, "bt-run-awaiting")
	recordLaunchEvent(t, repo, "bt-run-awaiting", core.TriggerEventKindBuilderTest, nil, now)
	insertTestApproval(t, repo, "appr-bt-run-awaiting", "bt-run-awaiting", 1)
	createActivityTestChat(t, repo, "interactive")
	recordLaunchEvent(t, repo, "interactive", core.TriggerEventKindChatStart, nil, now)

	for _, id := range []string{"bt-run", "bt-run-awaiting"} {
		chat, err := repo.GetChat(ctx, id)
		require.NoError(t, err)
		assert.False(t, chat.ListInSidebar, id)
	}

	project := "test-project"
	chats, err := repo.ListChats(ctx, ChatFilters{UserID: "test-user", ProjectID: &project, Limit: 100, SidebarOnly: true})
	require.NoError(t, err)
	got := map[string]bool{}
	for _, c := range chats {
		got[c.ID] = true
	}
	assert.True(t, got["interactive"])
	assert.False(t, got["bt-run"])
	assert.False(t, got["bt-run-awaiting"])
}

// With no launch kind named, the Runs list leaves builder tests out; naming
// builder.test brings back exactly them.
func TestListRuns_BuilderTestsExcludedByDefaultAndIncludedWhenFiltered(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	seedListRun(t, repo, listRunSeed{id: "real", workflow: "wf", status: Completed(), at: base.Add(1 * time.Minute), launch: core.TriggerEventKindChatStart})
	seedListRun(t, repo, listRunSeed{id: "legacy", workflow: "wf", status: Completed(), at: base.Add(2 * time.Minute)})
	seedListRun(t, repo, listRunSeed{id: "agent", workflow: "wf", status: Completed(), at: base.Add(3 * time.Minute), launch: core.TriggerEventKindAgentStartRun})
	seedListRun(t, repo, listRunSeed{id: "test", workflow: "wf", status: Completed(), at: base.Add(4 * time.Minute), launch: core.TriggerEventKindBuilderTest})

	list := func(kinds ...string) []string {
		items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50, LaunchKinds: kinds})
		require.NoError(t, err)
		return listRunIDs(items)
	}
	assert.Equal(t, []string{"agent", "legacy", "real"}, list(), "default list omits builder tests")
	assert.Equal(t, []string{"test"}, list("builder.test"))
	assert.Equal(t, []string{"test", "legacy", "real"}, list("builder.test", "chat.start"))

	// The per-workflow "last run" rollup is the same real-history view.
	last, err := repo.LastRunPerWorkflow(ctx, RunListFilters{UserID: "test-user"})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent"}, listRunIDs(last))
}
