// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func firingOf(id, chatID string, state core.RunDisplayState) *core.TriggerEventWithRun {
	c := chatID
	return &core.TriggerEventWithRun{
		Event: &core.TriggerEvent{ID: id, ChatID: &c, Outcome: core.TriggerEventLaunched, OccurredAt: time.Unix(0, 0)},
		Run:   &core.TriggerEventRun{ChatID: chatID, DisplayState: state},
	}
}

func TestFailureStreak_CountsUntilASuccessAndKeysOnTheOldestFailure(t *testing.T) {
	// newest first
	firings := []*core.TriggerEventWithRun{
		firingOf("e4", "c4", core.RunDisplayFailed),
		firingOf("e3", "c3", core.RunDisplayRunning), // still going: neither extends nor ends
		firingOf("e2", "c2", core.RunDisplayFailed),
		firingOf("e1", "c1", core.RunDisplayCompleted),
		firingOf("e0", "c0", core.RunDisplayFailed), // before the success: not in the streak
	}
	s := FailureStreak(firings)
	assert.EqualValues(t, 2, s.Count)
	assert.Equal(t, "e2", s.First.Event.ID)
	assert.Equal(t, "e4", s.Newest.Event.ID)
	assert.Equal(t, FailureStreak(nil), Streak{})
}

func TestPriorFailureStreak_ExcludesTheRunItself(t *testing.T) {
	firings := []*core.TriggerEventWithRun{
		firingOf("e3", "c3", core.RunDisplayFailed),
		firingOf("e2", "c2", core.RunDisplayFailed),
		firingOf("e1", "c1", core.RunDisplayCompleted),
	}
	prior, found := PriorFailureStreak(firings, "c3")
	assert.True(t, found)
	assert.EqualValues(t, 1, prior.Count, "c2 failed before c3")

	prior, found = PriorFailureStreak(firings, "c2")
	assert.True(t, found)
	assert.EqualValues(t, 0, prior.Count, "c2 is the first failure; a success precedes it")

	_, found = PriorFailureStreak(firings, "nope")
	assert.False(t, found)
}
