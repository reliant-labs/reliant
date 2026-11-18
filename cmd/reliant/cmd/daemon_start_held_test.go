// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// #2823: a second `daemon start` pointed at a running daemon's data directory
// logged into it and overwrote its runtime record before the runtime's own
// claim refused it. Start now refuses first, naming the incumbent.
func TestRefuseHeldDataDirRefusesALiveDaemonsDirectory(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := daemonstate.Acquire(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })
	writeRecordForPID(t, dataDir, 4242)

	err = refuseHeldDataDir(dataDir)

	require.Error(t, err)
	require.True(t, errors.Is(err, daemonstate.ErrLocked), "got %v", err)
	require.Contains(t, err.Error(), "daemon PID 4242")
	require.Contains(t, err.Error(), dataDir)
}

func TestRefuseHeldDataDirAllowsAnUnheldDirectory(t *testing.T) {
	fresh := t.TempDir()
	require.NoError(t, refuseHeldDataDir(fresh), "a directory no daemon ever claimed")

	released := t.TempDir()
	lock, err := daemonstate.Acquire(released)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
	require.NoError(t, refuseHeldDataDir(released), "a claim released by a daemon that exited")

	_, statErr := os.Stat(fresh + "/" + daemonstate.LockFileName)
	require.True(t, os.IsNotExist(statErr), "the check must not create a lock file")
}
