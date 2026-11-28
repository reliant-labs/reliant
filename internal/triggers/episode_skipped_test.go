// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
)

// A run that completed into a declared outcome: skipped (run_outcome
// skipped) chose not to do its work. To the trigger's health it is neither a
// success nor a failure, so against the real store it must not end the
// failure episode the Inbox counts: a failure, a skip, then a failure is one
// streak of two that still begins at the first failure. Only a real success
// ends it.
func TestFailureEpisode_ASkipDoesNotEndIt(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	const user, project = "test-user", "test-project"
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	tr := &core.Trigger{
		ID: "trg-" + uuid.NewString()[:8], UserID: user, ProjectID: project, Name: "episode",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "nightly", Message: "go", DaemonID: "d",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, triggertest.Create(ctx, repo, tr))
	base := now.Add(-time.Hour)

	// launch records a launched firing whose run ended with status and outcome.
	launch := func(id string, at time.Time, status, outcome string) {
		t.Helper()
		chatID := "chat-" + id
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: project, UserID: user, WorkflowID: &chatID}))
		ev := &core.TriggerEvent{
			ID: id, TriggerID: &tr.ID, UserID: user, Kind: core.TriggerEventKindSchedule,
			DedupeKey: "dd-" + id, OccurredAt: at, Outcome: core.TriggerEventLaunched, ChatID: &chatID, Payload: map[string]any{},
		}
		created, err := repo.CreateTriggerEvent(ctx, ev)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, store.SetLaunchEventRunStatus(ctx, repo.ORM(), chatID, status))
		require.NoError(t, store.SetLaunchEventRunOutcome(ctx, repo.ORM(), chatID, outcome))
	}
	episode := func() ([]string, Streak) {
		t.Helper()
		got, err := repo.FiringsSinceLastSuccess(ctx, user, []string{tr.ID}, 500)
		require.NoError(t, err)
		ids := make([]string, 0, len(got[tr.ID]))
		for _, f := range got[tr.ID] {
			ids = append(ids, f.Event.ID)
		}
		return ids, FailureStreak(got[tr.ID])
	}

	launch("ev-0-ok", base, core.TriggerRunCompleted, core.RunOutcomeSucceeded)
	launch("ev-1-fail", base.Add(1*time.Minute), core.TriggerRunFailed, core.RunOutcomeFailed)
	launch("ev-2-skip", base.Add(2*time.Minute), core.TriggerRunCompleted, core.RunOutcomeSkipped)
	launch("ev-3-fail", base.Add(3*time.Minute), core.TriggerRunFailed, core.RunOutcomeFailed)
	launch("ev-4-skip", base.Add(4*time.Minute), core.TriggerRunCompleted, core.RunOutcomeSkipped)

	ids, streak := episode()
	assert.Equal(t, []string{"ev-4-skip", "ev-3-fail", "ev-2-skip", "ev-1-fail"}, ids,
		"a skip does not end the episode; the success before it does")
	assert.EqualValues(t, 2, streak.Count, "the skips neither end nor extend the streak")
	require.NotNil(t, streak.First)
	assert.Equal(t, "ev-1-fail", streak.First.Event.ID, "the episode began at the first failure")
	require.NotNil(t, streak.Newest)
	assert.Equal(t, "ev-3-fail", streak.Newest.Event.ID)

	launch("ev-5-ok", base.Add(5*time.Minute), core.TriggerRunCompleted, core.RunOutcomeSucceeded)
	ids, streak = episode()
	assert.Empty(t, ids, "nothing has failed since the success")
	assert.Zero(t, streak.Count)
}
