// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

type fakeSwap struct {
	armed    bool
	execErr  error
	execs    int
	disabled error
}

func (f *fakeSwap) Armed() bool { return f.armed }
func (f *fakeSwap) ExecPending() error {
	f.execs++
	f.armed = false
	return f.execErr
}
func (f *fakeSwap) Disable(err error) { f.disabled = err }

// A runtime that stopped for a self-update hands the swap to `daemon start`,
// after the background processes are stopped. A swap that fails leaves the
// old binary in place, so the runtime runs again on it, with auto-update off,
// instead of leaving the machine with no daemon.
func TestRunSelfUpdatingSwapsAndRecoversFromAFailedSwap(t *testing.T) {
	upgradeExit := fmt.Errorf("tools-daemon: %w", daemonruntime.ErrUpgradeExit)

	swap := &fakeSwap{armed: true, execErr: errors.New("no space left on device")}
	starts, killed := 0, 0
	err := runSelfUpdating(func() error {
		starts++
		if starts == 1 {
			return upgradeExit
		}
		return nil
	}, swap, func() { killed++ })
	require.NoError(t, err)
	require.Equal(t, 2, starts, "the runtime runs again on the old binary")
	require.Equal(t, 1, swap.execs)
	require.Equal(t, 1, killed, "background processes are stopped before the exec")
	require.ErrorContains(t, swap.disabled, "no space left")

	// An upgrade exit with no swap armed (a managed drain's) is returned as is.
	swap = &fakeSwap{}
	require.ErrorIs(t, runSelfUpdating(func() error { return upgradeExit }, swap, func() {}), daemonruntime.ErrUpgradeExit)
	require.Zero(t, swap.execs)

	// No updater at all: the runtime's error is the answer.
	require.ErrorIs(t, runSelfUpdating(func() error { return upgradeExit }, nil, func() {}), daemonruntime.ErrUpgradeExit)
}
