// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

const (
	activityRunning         = 1
	activityAwaitingInput   = 2
	activityWaitingOnDaemon = 5
)

func activityOf(t *testing.T, repo *Repo, chatID string) int {
	t.Helper()
	chat, err := repo.GetChat(context.Background(), chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.Activity)
	return *chat.Activity
}

// AWAITING_INPUT beats WAITING_FOR_DAEMON, which beats RUNNING.
func TestActivityPrecedence_WaitingForDaemon(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	createActivityTestChat(t, repo, "pend")
	insertTestWorkflow(t, repo, "pend", "pend", "builtin://agent", Active())
	require.Equal(t, activityRunning, activityOf(t, repo, "pend"))

	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend", true))
	assert.Equal(t, activityWaitingOnDaemon, activityOf(t, repo, "pend"), "blocked beats running")

	insertTestApproval(t, repo, "pend-approval", "pend", 1)
	assert.Equal(t, activityAwaitingInput, activityOf(t, repo, "pend"), "awaiting input beats blocked")
}

func TestWaitingForDaemon_ClearsOnSuccessAndNeedsALiveRun(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	createActivityTestChat(t, repo, "pend-clear")
	insertTestWorkflow(t, repo, "pend-clear", "pend-clear", "builtin://agent", Active())
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-clear", true))
	require.Equal(t, activityWaitingOnDaemon, activityOf(t, repo, "pend-clear"))

	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-clear", false))
	assert.Equal(t, activityRunning, activityOf(t, repo, "pend-clear"))

	// A marker left behind on a run that has since stopped must not read as live.
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-clear", true))
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, "pend-clear", Completed()))
	assert.NotEqual(t, activityWaitingOnDaemon, activityOf(t, repo, "pend-clear"))
}

func TestSetChatDaemonBlocked_EmitsOnlyOnTransition(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "pend-emit")
	insertTestWorkflow(t, repo, "pend-emit", "pend-emit", "builtin://agent", Active())

	count := func() int { return len(activityUpdatesForChat(t, repo, "pend-emit")) }
	base := count()

	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-emit", true))
	require.Equal(t, base+1, count())
	updates := activityUpdatesForChat(t, repo, "pend-emit")
	assert.Equal(t, activityWaitingOnDaemon, activityValueFromUpdate(t, updates[len(updates)-1]))

	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-emit", true))
	assert.Equal(t, base+1, count(), "re-setting an already set marker is not a transition")

	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-emit", false))
	assert.Equal(t, base+2, count())
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "pend-emit", false))
	assert.Equal(t, base+2, count(), "a healthy tool call on an unblocked chat emits nothing")
}

func TestListRuns_ReportsWaitingForMachine(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	seedListRun(t, repo, listRunSeed{id: "wait", status: Active()})
	seedListRun(t, repo, listRunSeed{id: "plain", status: Active()})
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "wait", true))

	items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	got := map[string]RunDisplayState{}
	for _, it := range items {
		got[it.ChatID] = it.DisplayState
	}
	assert.Equal(t, core.RunDisplayWaitingForMachine, got["wait"])
	assert.Equal(t, RunDisplayRunning, got["plain"])

	items, _, err = repo.ListRuns(ctx, RunListFilters{
		UserID: "test-user", Limit: 50, DisplayStates: []RunDisplayState{RunDisplayWaitingForMachine},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"wait"}, listRunIDs(items))

	// LastRunPerWorkflow carries its own copy of the display_state CASE.
	seedListRun(t, repo, listRunSeed{id: "wait-last", workflow: "builtin://only-one", status: Active()})
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "wait-last", true))
	last, err := repo.LastRunPerWorkflow(ctx, RunListFilters{UserID: "test-user"})
	require.NoError(t, err)
	var found bool
	for _, it := range last {
		if it.ChatID == "wait-last" {
			found = true
			assert.Equal(t, core.RunDisplayWaitingForMachine, it.DisplayState)
		}
	}
	require.True(t, found, "the run must be returned for this assertion to mean anything")
}
