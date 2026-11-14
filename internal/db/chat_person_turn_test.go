// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// chat_person_turns on the migrated schema: one row per chat, the latest
// person turn wins, and the row goes with its chat (org-provider-accounts
// §11.1, I2).
func TestChatPersonTurns(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	countRows := func(chatID string) int {
		t.Helper()
		n, err := repo.ORM().Bun().NewSelect().Table("chat_person_turns").Where("chat_id = ?", chatID).Count(ctx)
		require.NoError(t, err)
		return int(n)
	}

	alice := seedChatForRun(t, repo, ctx, "alice")
	bob := seedChatForRun(t, repo, ctx, "bob")

	// Absent: nobody has taken a turn.
	at, err := repo.ChatPersonTurnAt(ctx, alice)
	require.NoError(t, err)
	assert.Nil(t, at)

	// Recorded at a fixed time, read back exactly.
	first := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, store.RecordChatPersonTurn(ctx, repo.ORM(), alice, "alice", first))
	at, err = repo.ChatPersonTurnAt(ctx, alice)
	require.NoError(t, err)
	require.NotNil(t, at)
	assert.True(t, at.Equal(first), "got %v", at)

	// The same chat again: one row, the later turn.
	later := first.Add(time.Hour)
	require.NoError(t, store.RecordChatPersonTurn(ctx, repo.ORM(), alice, "alice", later))
	assert.Equal(t, 1, countRows(alice))
	at, err = repo.ChatPersonTurnAt(ctx, alice)
	require.NoError(t, err)
	assert.True(t, at.Equal(later), "got %v", at)

	// Isolation: another chat and person are untouched, and their own row
	// is theirs.
	at, err = repo.ChatPersonTurnAt(ctx, bob)
	require.NoError(t, err)
	assert.Nil(t, at, "a turn in alice's chat says nothing of bob's")
	require.NoError(t, repo.RecordChatPersonTurn(ctx, bob, "bob"))
	var users []string
	require.NoError(t, repo.ORM().Bun().NewSelect().Table("chat_person_turns").Column("user_id").
		Where("chat_id IN (?, ?)", alice, bob).Order("user_id").Scan(ctx, &users))
	assert.Equal(t, []string{"alice", "bob"}, users)

	// A chat's row goes with it.
	require.NoError(t, repo.DeleteChat(ctx, alice))
	assert.Zero(t, countRows(alice), "deleting the chat cascades")
	assert.Equal(t, 1, countRows(bob))
}
