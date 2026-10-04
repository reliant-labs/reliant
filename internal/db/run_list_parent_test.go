// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func agentRun(id, parent, user string) listRunSeed {
	return listRunSeed{
		id: id, user: user, status: Active(), title: "child " + id,
		launch:  core.TriggerEventKindAgentStartRun,
		payload: map[string]any{"parent_chat_id": parent},
	}
}

func TestListRuns_ParentChatFilterReturnsExactlyTheUsersChildren(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	seedListRun(t, repo, listRunSeed{id: "parent-a", title: "Parent A", status: Active()})
	seedListRun(t, repo, listRunSeed{id: "parent-b", title: "Parent B", status: Active()})
	seedListRun(t, repo, agentRun("kid-1", "parent-a", ""))
	seedListRun(t, repo, agentRun("kid-2", "parent-a", ""))
	seedListRun(t, repo, agentRun("other-kid", "parent-b", ""))
	// A schedule run that happens to carry the same payload key must not match.
	seedListRun(t, repo, listRunSeed{id: "sched", status: Active(), launch: core.TriggerEventKindSchedule,
		payload: map[string]any{"parent_chat_id": "parent-a"}})
	// Another user's agent run naming the same parent chat id is not ours.
	seedListRun(t, repo, agentRun("foreign-kid", "parent-a", "someone-else"))

	parent := "parent-a"
	items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", ParentChatID: &parent, Limit: 50})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"kid-1", "kid-2"}, listRunIDs(items))

	none := "nobody"
	items, _, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", ParentChatID: &none, Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, items)

	items, _, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	assert.Len(t, items, 6, "unfiltered list is unchanged: both parents, three own kids, the schedule run")
}

func TestListRuns_ParentChatIDAndTitleArePopulated(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	seedListRun(t, repo, listRunSeed{id: "parent", title: "Refactor the billing module", status: Active()})
	seedListRun(t, repo, agentRun("kid", "parent", ""))
	seedListRun(t, repo, agentRun("orphan", "deleted-parent", ""))
	seedListRun(t, repo, listRunSeed{id: "foreign-parent", user: "someone-else", title: "Secret title", status: Active()})
	seedListRun(t, repo, agentRun("snoop", "foreign-parent", ""))

	items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	byID := map[string]*RunListItem{}
	for _, item := range items {
		byID[item.ChatID] = item
	}
	assert.Equal(t, "parent", byID["kid"].ParentChatID)
	assert.Equal(t, "Refactor the billing module", byID["kid"].ParentChatTitle)
	assert.Equal(t, "deleted-parent", byID["orphan"].ParentChatID)
	assert.Empty(t, byID["orphan"].ParentChatTitle, "a parent that no longer exists has no title")
	assert.Equal(t, "foreign-parent", byID["snoop"].ParentChatID)
	assert.Empty(t, byID["snoop"].ParentChatTitle, "another user's chat title must not leak")
	assert.Empty(t, byID["parent"].ParentChatID, "an interactive run has no parent")

	last, err := repo.LastRunPerWorkflow(ctx, RunListFilters{UserID: "test-user"})
	require.NoError(t, err)
	require.Len(t, last, 1)
}
