// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPausedChat is a chat of userID whose root run (id = chat id) has status.
func newPausedChat(t *testing.T, repo *Repo, raw *sql.DB, chatID, userID string, status WorkflowStatus) {
	t.Helper()
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, chatID, chatID, "builtin://agent", status)
	_, err := raw.Exec(`UPDATE chats SET workflow_id = $1, user_id = $2 WHERE id = $1`, chatID, userID)
	require.NoError(t, err)
}

func pausedIDs(t *testing.T, repo *Repo, userID string) []string {
	t.Helper()
	chats, err := repo.ListChatsPausedForMachine(context.Background(), userID, 100)
	require.NoError(t, err)
	ids := make([]string, 0, len(chats))
	for _, c := range chats {
		ids = append(ids, c.ChatID)
	}
	return ids
}

// The list holds only chats the breaker marked whose root run still reads
// paused, scoped to a user when one is given.
func TestListChatsPausedForMachine(t *testing.T) {
	repo, raw, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	newPausedChat(t, repo, raw, "pm-breaker", "u1", Paused())
	newPausedChat(t, repo, raw, "pm-user-paused", "u1", Paused()) // never marked
	newPausedChat(t, repo, raw, "pm-other-user", "u2", Paused())
	newPausedChat(t, repo, raw, "pm-ran-since", "u1", Active()) // resumed; stale mark
	newPausedChat(t, repo, raw, "pm-archived", "u1", Paused())
	for _, id := range []string{"pm-breaker", "pm-other-user", "pm-ran-since", "pm-archived"} {
		changed, err := repo.SetChatPausedForMachine(ctx, id, true)
		require.NoError(t, err)
		require.True(t, changed)
	}
	_, err := raw.Exec(`UPDATE chats SET state = 3 WHERE id = 'pm-archived'`)
	require.NoError(t, err)

	assert.Equal(t, []string{"pm-breaker"}, pausedIDs(t, repo, "u1"))
	assert.ElementsMatch(t, []string{"pm-breaker", "pm-other-user"}, pausedIDs(t, repo, ""),
		"an empty user lists every user's (the sweep)")

	chats, err := repo.ListChatsPausedForMachine(ctx, "u1", 100)
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, "pm-breaker", chats[0].WorkflowID, "the paused root run, for the resume")
	assert.Equal(t, "u1", chats[0].UserID)
	assert.False(t, chats[0].PausedAt.IsZero())
}

// Clearing the mark is a claim: of two callers that race exactly one sees the
// change, which is what makes a double connect resume once.
func TestSetChatPausedForMachine_ClearIsAClaim(t *testing.T) {
	repo, raw, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	newPausedChat(t, repo, raw, "pm-claim", "u1", Paused())

	set, err := repo.SetChatPausedForMachine(ctx, "pm-claim", true)
	require.NoError(t, err)
	assert.True(t, set)
	again, err := repo.SetChatPausedForMachine(ctx, "pm-claim", true)
	require.NoError(t, err)
	assert.False(t, again, "marking an already-marked chat changes nothing")

	first, err := repo.SetChatPausedForMachine(ctx, "pm-claim", false)
	require.NoError(t, err)
	second, err := repo.SetChatPausedForMachine(ctx, "pm-claim", false)
	require.NoError(t, err)
	assert.True(t, first)
	assert.False(t, second)
	assert.Empty(t, pausedIDs(t, repo, "u1"))
}
