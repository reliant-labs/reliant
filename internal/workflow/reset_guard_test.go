// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResetAttemptGuard_BoundsWithoutProgress(t *testing.T) {
	g := NewResetAttemptGuard(2)
	const wf = "wf-1"

	// Each re-failure lands at ~the same history length (no forward progress).
	assert.True(t, g.Allow(wf, 100), "first reset allowed")
	g.Record(wf, 100)

	assert.True(t, g.Allow(wf, 100), "second reset allowed (1 < max)")
	g.Record(wf, 100)

	assert.False(t, g.Allow(wf, 100), "third reset denied (bound reached without progress)")
	assert.Equal(t, 2, g.Attempts(wf))
}

func TestResetAttemptGuard_ProgressClearsStreak(t *testing.T) {
	g := NewResetAttemptGuard(2)
	const wf = "wf-1"

	g.Record(wf, 100)
	g.Record(wf, 100)
	assert.False(t, g.Allow(wf, 100), "bound reached at same history length")

	// A later interruption whose run progressed further (history grew) is a new
	// incident — the streak clears and resets are allowed again.
	assert.True(t, g.Allow(wf, 250), "forward progress clears the streak")
	assert.Equal(t, 0, g.Attempts(wf), "streak dropped after progress")
}

func TestResetAttemptGuard_ClearAndPrune(t *testing.T) {
	g := NewResetAttemptGuard(1)
	g.Record("a", 10)
	g.Record("b", 10)
	assert.Equal(t, 1, g.Attempts("a"))

	g.Clear("a")
	assert.Equal(t, 0, g.Attempts("a"))

	g.Prune(map[string]bool{}) // nothing running → drop all
	assert.Equal(t, 0, g.Attempts("b"))
}

func TestResetAttemptGuard_NilIsAlwaysAllow(t *testing.T) {
	var g *ResetAttemptGuard
	assert.True(t, g.Allow("wf", 0), "nil guard always allows")
	g.Record("wf", 0) // no panic
	assert.Equal(t, 0, g.Attempts("wf"))
	g.Clear("wf")
	g.Prune(nil)
}

func TestResetAttemptGuard_DefaultMax(t *testing.T) {
	g := NewResetAttemptGuard(0) // non-positive → DefaultMaxResetAttempts
	const wf = "wf-1"
	for i := 0; i < DefaultMaxResetAttempts; i++ {
		assert.True(t, g.Allow(wf, 50), "attempt %d allowed", i)
		g.Record(wf, 50)
	}
	assert.False(t, g.Allow(wf, 50), "denied after DefaultMaxResetAttempts")
}
