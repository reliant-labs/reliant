// Copyright (c) 2025 Reliant Labs
package machinefailure

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run of Failed looks keeps the start of its first look while it is
// unbroken; any other state clears it, so a machine that left Failed and came
// back to it starts over.
func TestRunStart(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 18, 21, 3, 0, time.UTC)

	since := RunStart(time.Time{}, true, t0)
	require.Equal(t, t0, since, "the first Failed look starts the run")
	assert.Equal(t, t0, RunStart(since, true, t0.Add(time.Minute)), "an unbroken run keeps its start")
	assert.True(t, RunStart(since, false, t0.Add(time.Minute)).IsZero(), "any other state clears it")
	assert.True(t, RunStart(time.Time{}, false, t0).IsZero(), "no run, no start")
}

// Failed is believed only once it has held for the settle window.
func TestBelieved(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 18, 21, 3, 0, time.UTC)

	assert.False(t, Believed(time.Time{}, t0, Settle), "no run is never believed")
	assert.False(t, Believed(t0, t0, Settle), "the first look is not believed")
	assert.False(t, Believed(t0, t0.Add(40*time.Second), Settle), "a VM death's ~40s is not believed")
	assert.True(t, Believed(t0, t0.Add(Settle), Settle), "a run that held for the window is")
	assert.True(t, Believed(t0, t0.Add(time.Hour), Settle))
}

// The prod 2026-10-30 shape, look by look: Failed for 17s, then starting,
// then Failed again. The second Failed run starts over, so nothing is
// believed even though Failed was seen across more than the window.
func TestRunStart_RecoveringSequenceIsNeverBelieved(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 18, 21, 3, 0, time.UTC)
	looks := []struct {
		at     time.Duration
		failed bool
	}{
		{0, true}, {17 * time.Second, true}, {30 * time.Second, false},
		{100 * time.Second, true}, {150 * time.Second, true},
	}
	var since time.Time
	for _, look := range looks {
		now := t0.Add(look.at)
		since = RunStart(since, look.failed, now)
		assert.False(t, Believed(since, now, Settle), "look at +%s", look.at)
	}
}
