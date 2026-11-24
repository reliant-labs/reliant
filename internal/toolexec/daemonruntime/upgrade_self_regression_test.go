// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelfUpdateCountsAndDrainsCommands(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{})
	d := s.d
	require.True(t, d.upgrade.beginCommand())
	s.clock.advance(2 * selfQuietIdle)
	require.False(t, d.quiet(selfQuietIdle), "a long exec remains active")
	_, err := d.selfDrain(context.Background(), s.clock.read(), false, "v2")
	require.ErrorIs(t, err, errSelfDrainDeclined)
	d.upgrade.mu.Lock()
	d.upgrade.draining = true
	d.upgrade.mu.Unlock()
	require.False(t, d.upgrade.beginCommand(), "new commands cannot enter a drain")
	d.upgrade.endCommand()
	require.Zero(t, d.runningWork())
	require.False(t, d.quiet(selfQuietIdle), "completion resets idle time")
	s.clock.advance(selfQuietIdle)
	require.True(t, d.quiet(selfQuietIdle))
}

func TestQuietWaitRefreshesBeforeInstalling(t *testing.T) {
	for _, wake := range []bool{false, true} {
		s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{})
		refresh := make(chan time.Time, 1)
		if wake {
			s.d.upgrade.wakeCh() <- struct{}{}
		} else {
			refresh <- selfEpoch
		}
		// Even an already quiet daemon must refresh before using the candidate.
		s.clock.advance(2 * selfQuietIdle)
		_, ok := s.d.waitForQuiet(context.Background(), selfQuietIdle, refresh)
		require.False(t, ok)
	}
}

func TestUpgradeBackstopDoesNotKillRecoveredRuntime(t *testing.T) {
	var fire func()
	var exits int
	u := &upgradeDrain{
		exit:      func(int) { exits++ },
		afterFunc: func(_ time.Duration, f func()) { fire = f },
	}
	u.upgradeExit()
	require.NotNil(t, fire)
	// Start has completed its teardown; a failed ExecPending can now recover.
	u.teardownDone.Store(true)
	fire()
	require.Zero(t, exits)
}
