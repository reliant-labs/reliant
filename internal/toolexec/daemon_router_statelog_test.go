// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMachineStateLog_Throttles(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	log := newMachineStateLog(time.Minute)
	log.now = func() time.Time { return now }

	ok, suppressed := log.admit("starting|u1")
	require.True(t, ok)
	assert.Zero(t, suppressed)

	for request := 0; request < 62; request++ {
		now = now.Add(time.Second / 2)
		ok, _ = log.admit("starting|u1")
		assert.False(t, ok, "request %d within the window was written", request)
	}

	ok, _ = log.admit("starting|u2")
	assert.True(t, ok)
	ok, _ = log.admit("absent|u1")
	assert.True(t, ok)

	now = now.Add(time.Minute)
	ok, suppressed = log.admit("starting|u1")
	require.True(t, ok)
	assert.Equal(t, 62, suppressed)
}

func TestMachineStateLog_HardBoundsFreshEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	log := newMachineStateLog(time.Hour)
	log.now = func() time.Time { return now }

	for index := 0; index < machineStateLogMaxKeys; index++ {
		ok, _ := log.admit(fmt.Sprintf("starting|u%d", index))
		require.True(t, ok)
		now = now.Add(time.Nanosecond)
	}

	ok, _ := log.admit("starting|fresh")
	require.True(t, ok)
	require.Len(t, log.entries, machineStateLogMaxKeys)
	_, oldestStillPresent := log.entries["starting|u0"]
	assert.False(t, oldestStillPresent, "fresh entries must evict the oldest at the cap")
	_, freshPresent := log.entries["starting|fresh"]
	assert.True(t, freshPresent)
}
