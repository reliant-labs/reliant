// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pinning a daemon is "Connect a machine" for a chat with no machine
// (research/NO_MACHINE_CHATS.md): it ends no-machine in the same write, and
// clearing the daemon afterwards does not bring no-machine back. Before, the
// pin left no_machine true, so a chat the user had connected kept running with
// no machine and was refused every tool the machine could have run.
func TestUpdateChatActiveDaemon_PinningEndsNoMachineAndClearingNeverRestoresIt(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	createActivityTestChat(t, repo, "nm-pin")
	_, err := raw.Exec(`UPDATE chats SET no_machine = true WHERE id = $1`, "nm-pin")
	require.NoError(t, err)

	daemonID := "daemon-nm-pin"
	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, "nm-pin", &daemonID))
	chat, err := repo.GetChat(ctx, "nm-pin")
	require.NoError(t, err)
	assert.False(t, chat.NoMachine, "pinning a daemon puts the chat on a machine")
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, daemonID, *chat.ActiveDaemonID)

	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, "nm-pin", nil))
	chat, err = repo.GetChat(ctx, "nm-pin")
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
	assert.False(t, chat.NoMachine, "a chat that has had a machine never becomes one without")
}

// Clearing the daemon of a chat that never had one leaves it as it was: the
// query only ever turns no_machine off.
func TestUpdateChatActiveDaemon_ClearingLeavesNoMachineAlone(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	createActivityTestChat(t, repo, "nm-clear")
	_, err := raw.Exec(`UPDATE chats SET no_machine = true WHERE id = $1`, "nm-clear")
	require.NoError(t, err)

	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, "nm-clear", nil))
	chat, err := repo.GetChat(ctx, "nm-clear")
	require.NoError(t, err)
	assert.True(t, chat.NoMachine)
}

// A chat with no machine that pins a daemon is a contradiction the schema
// refuses, whatever path writes it.
func TestChats_NoMachineWithAnActiveDaemonIsRefused(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	createActivityTestChat(t, repo, "nm-check")
	daemonID := "daemon-nm-check"
	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, "nm-check", &daemonID))

	_, err := raw.Exec(`UPDATE chats SET no_machine = true WHERE id = $1`, "nm-check")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chats_no_machine_has_no_daemon_check")
}
