// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The write that decides a chat's machine once (research/CHAT_MACHINE_PLACEMENT.md):
// it sets the pin only while there is none, so a second pinner finds the first's.
func TestPinChatDaemonIfUnset_FirstWriterWins(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "pin-first")

	stored, err := repo.PinChatDaemonIfUnset(ctx, "pin-first", "vm")
	require.NoError(t, err)
	assert.Equal(t, "vm", stored)

	stored, err = repo.PinChatDaemonIfUnset(ctx, "pin-first", "laptop")
	require.NoError(t, err)
	assert.Equal(t, "vm", stored, "the caller uses what the row holds, not what it asked for")

	chat, err := repo.GetChat(ctx, "pin-first")
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, "vm", *chat.ActiveDaemonID)
}

// Of many sends racing to pin the same chat, every one returns the same machine.
func TestPinChatDaemonIfUnset_ConcurrentSendsAgree(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "pin-race")

	const racers = 8
	results := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.PinChatDaemonIfUnset(ctx, "pin-race", "daemon-"+string(rune('a'+i)))
		}(i)
	}
	wg.Wait()

	for i := range errs {
		require.NoError(t, errs[i])
		assert.Equal(t, results[0], results[i], "every racer reads back the one stored pin")
	}
	chat, err := repo.GetChat(ctx, "pin-race")
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, results[0], *chat.ActiveDaemonID)
}

// A chat with no machine by design never gets one from a default: the
// constraint forbids it and the pin must not even try.
func TestPinChatDaemonIfUnset_NeverPinsANoMachineChat(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "pin-nomachine")
	_, err := raw.Exec(`UPDATE chats SET no_machine = true WHERE id = $1`, "pin-nomachine")
	require.NoError(t, err)

	stored, err := repo.PinChatDaemonIfUnset(ctx, "pin-nomachine", "vm")
	require.NoError(t, err)
	assert.Empty(t, stored)

	chat, err := repo.GetChat(ctx, "pin-nomachine")
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
	assert.True(t, chat.NoMachine)
}

// A pin the write makes is announced like SetChatDaemon's, so open clients
// show the machine; one that was already there, or lost, is not.
func TestPinChatDaemonIfUnset_AnnouncesOnlyTheWrite(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "pin-announce")

	count := func() int {
		var n int
		require.NoError(t, raw.QueryRow(
			`SELECT count(*) FROM user_updates WHERE chat_id = $1 AND data::text LIKE '%active_daemon_id%'`, "pin-announce").Scan(&n))
		return n
	}
	require.Zero(t, count())

	_, err := repo.PinChatDaemonIfUnset(ctx, "pin-announce", "vm")
	require.NoError(t, err)
	assert.Equal(t, 1, count())

	_, err = repo.PinChatDaemonIfUnset(ctx, "pin-announce", "laptop")
	require.NoError(t, err)
	assert.Equal(t, 1, count(), "a pin that was already there is not announced again")
}

// A chat that does not exist is not an error and holds no pin.
func TestPinChatDaemonIfUnset_MissingChat(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	stored, err := repo.PinChatDaemonIfUnset(context.Background(), "no-such-chat", "vm")
	require.NoError(t, err)
	assert.Empty(t, stored)
}
