// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// #2823: a second `daemon start` pointed at a running daemon's data directory
// logged into it and overwrote its runtime record before the runtime's own
// claim refused it. Start now refuses first, naming the incumbent.
func TestClaimDataDirRefusesALiveDaemonsDirectory(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := daemonstate.Acquire(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })
	writeRecordForPID(t, dataDir, 4242)

	claim, err := claimDataDir(dataDir)

	require.Error(t, err)
	require.Nil(t, claim)
	require.True(t, errors.Is(err, daemonstate.ErrLocked), "got %v", err)
	require.Contains(t, err.Error(), "daemon PID 4242")
	require.Contains(t, err.Error(), dataDir)
}

func TestClaimDataDirAllowsAnUnheldDirectory(t *testing.T) {
	fresh := t.TempDir()
	claim, err := claimDataDir(fresh)
	require.NoError(t, err, "a directory no daemon ever claimed")
	require.NoError(t, claim.Release())

	released := t.TempDir()
	lock, err := daemonstate.Acquire(released)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
	claim, err = claimDataDir(released)
	require.NoError(t, err, "a claim released by a daemon that exited")
	require.NoError(t, claim.Release())
}

// TL-48: a daemon still waiting to be signed in or paired held nothing, so a
// second `daemon start` in its directory ran alongside it. The first start's
// claim now holds from before it waits, and the refusal says it is waiting.
func TestClaimDataDirHoldsWhileWaitingForCredentials(t *testing.T) {
	dataDir := t.TempDir()
	first, err := claimDataDir(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })
	require.NoError(t, daemonstate.SetStream(dataDir, daemonstate.StreamAwaitingCredentials, "waiting"))

	second, err := claimDataDir(dataDir)

	require.Nil(t, second)
	require.ErrorIs(t, err, daemonstate.ErrLocked)
	require.Contains(t, err.Error(), "waiting to be signed in or paired")

	// Released for the runtime, the directory is the runtime's to claim.
	require.NoError(t, first.Release())
	runtimeClaim, err := daemonstate.Acquire(dataDir)
	require.NoError(t, err)
	require.NoError(t, runtimeClaim.Release())
}
