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

// A run that completed only because its failure path ran (run_outcome
// failure_handled, reliant#2753) is a failure to the trigger's health, so it
// must not end the failure episode the Inbox counts either: against the real
// store, an earlier failure followed by two handled-failure completions is one
// streak of three, and only a real success ends it. A completion recorded
// before run_outcome existed (NULL) still counts as a success.
func TestFailureEpisode_AHandledFailureDoesNotEndIt(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	const user, project = "test-user", "test-project"
	// A fixed clock: the episode is ordered by occurred_at alone, and nothing
	// here reads the wall clock.
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	tr := &core.Trigger{
		ID: "trg-" + uuid.NewString()[:8], UserID: user, ProjectID: project, Name: "episode",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "nightly", Message: "go", DaemonID: "d",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, triggertest.Create(ctx, repo, tr))
	base := now.Add(-time.Hour)

	fire := func(id string, at time.Time, outcome core.TriggerEventOutcome) {
		t.Helper()
		ev := &core.TriggerEvent{
			ID: id, TriggerID: &tr.ID, UserID: user, Kind: core.TriggerEventKindSchedule,
			DedupeKey: "dd-" + id, OccurredAt: at, Outcome: outcome, Payload: map[string]any{},
		}
		created, err := repo.CreateTriggerEvent(ctx, ev)
		require.NoError(t, err)
		require.True(t, created)
	}
	// launch records a launched firing whose run ended with status and
	// outcome ("" leaves run_outcome NULL, as before the column existed).
	launch := func(id string, at time.Time, outcome string) {
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
		require.NoError(t, store.SetLaunchEventRunStatus(ctx, repo.ORM(), chatID, core.TriggerRunCompleted))
		if outcome != "" {
			require.NoError(t, store.SetLaunchEventRunOutcome(ctx, repo.ORM(), chatID, outcome))
		}
	}
	episode := func() ([]string, Streak) {
		t.Helper()
		got, err := store.FiringsSinceLastSuccess(ctx, repo.ORM(), user, []string{tr.ID}, 500)
		require.NoError(t, err)
		ids := make([]string, 0, len(got[tr.ID]))
		for _, f := range got[tr.ID] {
			ids = append(ids, f.Event.ID)
		}
		return ids, FailureStreak(got[tr.ID])
	}

	// Before the episode: a failure, then a completion recorded before
	// run_outcome existed. That completion is a success and bounds the episode.
	fire("ev-0-older-fail", base, core.TriggerEventFailed)
	launch("ev-1-legacy-ok", base.Add(1*time.Minute), "")
	// The episode: a firing that could not launch, then two runs that
	// completed only because their failure paths ran.
	fire("ev-2-fail", base.Add(2*time.Minute), core.TriggerEventFailed)
	launch("ev-3-handled", base.Add(3*time.Minute), core.RunOutcomeFailureHandled)
	launch("ev-4-handled", base.Add(4*time.Minute), core.RunOutcomeFailureHandled)

	ids, streak := episode()
	assert.Equal(t, []string{"ev-4-handled", "ev-3-handled", "ev-2-fail"}, ids,
		"a handled failure does not end the episode; the legacy completion before it does")
	assert.EqualValues(t, 3, streak.Count, "the earlier failure and both handled failures are one streak")
	assert.EqualValues(t, 2, streak.RunFailures, "both handled failures launched a run")
	require.NotNil(t, streak.First)
	assert.Equal(t, "ev-2-fail", streak.First.Event.ID, "the episode began at the earlier failure")
	require.NotNil(t, streak.Newest)
	assert.Equal(t, "ev-4-handled", streak.Newest.Event.ID)

	// A real success ends the episode.
	launch("ev-5-ok", base.Add(5*time.Minute), core.RunOutcomeSucceeded)
	ids, streak = episode()
	assert.Empty(t, ids, "nothing has failed since the success")
	assert.Zero(t, streak.Count)
	assert.Nil(t, streak.First)
}
