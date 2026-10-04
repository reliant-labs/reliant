// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// list_in_sidebar is the one definition of "the sidebar lists this chat".
// WORKFLOW_UI.md §6 and §14.1 decisions 4 and 5.
func TestListInSidebarTruthTable(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	triggerID := "trg-sidebar"
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(triggerID, "test-user", "test-project", "nightly")))
	now := time.Now().UTC()

	launchKinds := []struct {
		name string
		kind core.TriggerEventKind // empty = no launch event (predates triggers)
	}{
		{"none", ""},
		{"chat.start", core.TriggerEventKindChatStart},
		{"schedule", core.TriggerEventKindSchedule},
		{"agent.start_run", core.TriggerEventKindAgentStartRun},
	}

	for _, lk := range launchKinds {
		for _, adopted := range []bool{false, true} {
			for _, awaiting := range []bool{false, true} {
				id := fmt.Sprintf("tt-%s-adopted=%t-awaiting=%t", lk.name, adopted, awaiting)
				createActivityTestChat(t, repo, id)
				if lk.kind != "" {
					var trigger *string
					if lk.kind == core.TriggerEventKindSchedule {
						trigger = &triggerID
					}
					recordLaunchEvent(t, repo, id, lk.kind, trigger, now)
				}
				if adopted {
					ok, err := repo.SetChatAdopted(ctx, id, "test-user", true)
					require.NoError(t, err)
					require.True(t, ok)
				}
				if awaiting {
					insertTestApproval(t, repo, "appr-"+id, id, 1)
				}

				// The documented policy, written independently of the SQL.
				// Awaiting input never lists a chat: since the Inbox shipped,
				// that is where an automation's question surfaces (§14.1
				// decision 5). Only origin and adoption decide.
				want := lk.kind == "" || lk.kind == core.TriggerEventKindChatStart || adopted

				chat, err := repo.GetChat(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, want, chat.ListInSidebar, id)
			}
		}
	}
}

func TestListChatsSidebarOnlyReadsTheViewColumn(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	createActivityTestChat(t, repo, "agent-run")
	recordLaunchEvent(t, repo, "agent-run", core.TriggerEventKindAgentStartRun, nil, now)
	createActivityTestChat(t, repo, "agent-run-adopted")
	recordLaunchEvent(t, repo, "agent-run-adopted", core.TriggerEventKindAgentStartRun, nil, now)
	_, err := repo.SetChatAdopted(ctx, "agent-run-adopted", "test-user", true)
	require.NoError(t, err)
	createActivityTestChat(t, repo, "interactive")
	// An automation asking a question is the Inbox's, not the sidebar's.
	createActivityTestChat(t, repo, "schedule-awaiting")
	recordLaunchEvent(t, repo, "schedule-awaiting", core.TriggerEventKindSchedule, nil, now)
	insertTestApproval(t, repo, "appr-schedule-awaiting", "schedule-awaiting", 1)
	// Adopted, it is the user's chat and stays listed while it waits.
	createActivityTestChat(t, repo, "schedule-awaiting-adopted")
	recordLaunchEvent(t, repo, "schedule-awaiting-adopted", core.TriggerEventKindSchedule, nil, now)
	insertTestApproval(t, repo, "appr-schedule-awaiting-adopted", "schedule-awaiting-adopted", 1)
	_, err = repo.SetChatAdopted(ctx, "schedule-awaiting-adopted", "test-user", true)
	require.NoError(t, err)

	project := "test-project"
	ids := func(sidebarOnly bool) map[string]bool {
		chats, err := repo.ListChats(ctx, ChatFilters{UserID: "test-user", ProjectID: &project, Limit: 100, SidebarOnly: sidebarOnly})
		require.NoError(t, err)
		out := map[string]bool{}
		for _, c := range chats {
			out[c.ID] = true
		}
		return out
	}
	assert.True(t, ids(false)["agent-run"])
	got := ids(true)
	assert.False(t, got["agent-run"], "an agent-started run is never listed unless adopted")
	assert.True(t, got["agent-run-adopted"])
	assert.True(t, got["interactive"])
	assert.True(t, ids(false)["schedule-awaiting"])
	assert.False(t, got["schedule-awaiting"], "an automation awaiting input is in the Inbox, not the sidebar")
	assert.True(t, got["schedule-awaiting-adopted"], "an adopted automation awaiting input stays listed")
}

func TestSetChatAdopted_OwnerScopedAndIdempotent(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "adopt-me")

	ok, err := repo.SetChatAdopted(ctx, "adopt-me", "someone-else", true)
	require.NoError(t, err)
	assert.False(t, ok, "another user cannot adopt the chat")
	chat, err := repo.GetChat(ctx, "adopt-me")
	require.NoError(t, err)
	assert.Nil(t, chat.AdoptedAt)

	ok, err = repo.SetChatAdopted(ctx, "no-such-chat", "test-user", true)
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = repo.SetChatAdopted(ctx, "adopt-me", "test-user", true)
	require.NoError(t, err)
	require.True(t, ok)
	chat, err = repo.GetChat(ctx, "adopt-me")
	require.NoError(t, err)
	require.NotNil(t, chat.AdoptedAt)
	first := *chat.AdoptedAt

	ok, err = repo.SetChatAdopted(ctx, "adopt-me", "test-user", true)
	require.NoError(t, err)
	assert.True(t, ok)
	chat, err = repo.GetChat(ctx, "adopt-me")
	require.NoError(t, err)
	assert.Equal(t, first, *chat.AdoptedAt, "adopting twice keeps the original timestamp")

	ok, err = repo.SetChatAdopted(ctx, "adopt-me", "someone-else", false)
	require.NoError(t, err)
	assert.False(t, ok)
	chat, _ = repo.GetChat(ctx, "adopt-me")
	assert.NotNil(t, chat.AdoptedAt, "another user cannot un-adopt it")

	for i := 0; i < 2; i++ {
		ok, err = repo.SetChatAdopted(ctx, "adopt-me", "test-user", false)
		require.NoError(t, err)
		assert.True(t, ok)
	}
	chat, _ = repo.GetChat(ctx, "adopt-me")
	assert.Nil(t, chat.AdoptedAt)
}

func TestSetChatAdopted_EmitsActivityChangedSoSidebarRefetches(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	createActivityTestChat(t, repo, "adopt-emit")
	before := len(activityUpdatesForChat(t, repo, "adopt-emit"))
	_, err := repo.SetChatAdopted(context.Background(), "adopt-emit", "test-user", true)
	require.NoError(t, err)
	assert.Equal(t, before+1, len(activityUpdatesForChat(t, repo, "adopt-emit")))
}
