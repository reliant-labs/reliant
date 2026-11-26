// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

var t0 = time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)

func checkRow() *core.WorkflowCheckRun {
	return &core.WorkflowCheckRun{ID: "c1", TriggerEventID: "e1", Name: "reliant/ci", Timeout: time.Hour, CreatedAt: t0}
}

func launched(root *core.WorkflowStatus) *core.WorkflowCheckObservation {
	return &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", Root: root}
}

func status(s core.WorkflowStatus) *core.WorkflowStatus { return &s }

// TestDeriveEveryPath pins what the check says for every way a firing and its
// run can stand: each ending concludes, and none concludes neutral or
// skipped, which a required check counts as passing.
func TestDeriveEveryPath(t *testing.T) {
	started := t0.Add(time.Minute)
	type tc struct {
		name       string
		row        func(*core.WorkflowCheckRun)
		obs        *core.WorkflowCheckObservation
		now        time.Time
		status     string
		conclusion string
		title      string
	}
	for _, c := range []tc{
		// Before the run.
		{name: "firing waits for capacity", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending},
			status: core.CheckStatusQueued, title: "Queued"},
		{name: "launched, no workflow row yet", obs: launched(nil), status: core.CheckStatusQueued},
		{name: "run pending", obs: launched(status(core.Pending())), status: core.CheckStatusQueued},
		{name: "run active, machine still waking", obs: launched(status(core.Active())), status: core.CheckStatusQueued},
		// Running.
		{name: "first node started", row: func(r *core.WorkflowCheckRun) { r.StartedAt = &started },
			obs: launched(status(core.Active())), status: core.CheckStatusInProgress, title: "Running"},
		{name: "paused, inside the grace", row: func(r *core.WorkflowCheckRun) { r.StartedAt = &started },
			obs: launched(status(core.Paused())), now: t0.Add(5 * time.Minute), status: core.CheckStatusInProgress, title: "Paused"},
		// Endings the run reported itself.
		{name: "reported completed", row: func(r *core.WorkflowCheckRun) { r.EndedStatus = core.TriggerRunCompleted },
			obs: launched(status(core.Active())), status: core.CheckStatusCompleted, conclusion: ConclusionSuccess},
		{name: "reported completed with success verdict", row: func(r *core.WorkflowCheckRun) { r.EndedStatus, r.Verdict = core.TriggerRunCompleted, "success" },
			obs: launched(nil), status: core.CheckStatusCompleted, conclusion: ConclusionSuccess},
		{name: "reported completed into a failure outcome", row: func(r *core.WorkflowCheckRun) { r.EndedStatus, r.Verdict = core.TriggerRunCompleted, "failure" },
			obs: launched(nil), status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		{name: "reported completed into a skipped outcome", row: func(r *core.WorkflowCheckRun) { r.EndedStatus, r.Verdict = core.TriggerRunCompleted, "skipped" },
			obs: launched(nil), status: core.CheckStatusCompleted, conclusion: ConclusionCancelled, title: "Skipped: the run skipped its work"},
		{name: "reported failed", row: func(r *core.WorkflowCheckRun) { r.EndedStatus = core.TriggerRunFailed },
			obs: launched(nil), status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		{name: "reported cancelled", row: func(r *core.WorkflowCheckRun) { r.EndedStatus = core.TriggerRunCancelled },
			obs: launched(nil), status: core.CheckStatusCompleted, conclusion: ConclusionCancelled},
		{name: "the run's report wins over a later human turn", row: func(r *core.WorkflowCheckRun) { r.EndedStatus = core.TriggerRunFailed },
			obs: launched(status(core.Completed())), status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		// Endings read from the trigger's record of its run.
		{name: "run_status completed", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", RunStatus: core.TriggerRunCompleted, Root: status(core.Active())},
			status: core.CheckStatusCompleted, conclusion: ConclusionSuccess},
		{name: "run_status completed with run_outcome skipped", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", RunStatus: core.TriggerRunCompleted, RunOutcome: core.RunOutcomeSkipped},
			status: core.CheckStatusCompleted, conclusion: ConclusionCancelled, title: "Skipped"},
		{name: "run_status failed", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", RunStatus: core.TriggerRunFailed},
			status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		{name: "run_status cancelled", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", RunStatus: core.TriggerRunCancelled},
			status: core.CheckStatusCompleted, conclusion: ConclusionCancelled},
		// Endings read from the workflow row (a run that died without reporting).
		{name: "row completed", obs: launched(status(core.Completed())), status: core.CheckStatusCompleted, conclusion: ConclusionSuccess},
		{name: "row completed into a failure outcome", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", Root: status(core.Completed()), RootVerdict: "failure"},
			status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		{name: "row completed into a skipped outcome", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", Root: status(core.Completed()), RootVerdict: "skipped"},
			status: core.CheckStatusCompleted, conclusion: ConclusionCancelled},
		{name: "row failed (engine error, reconciler)", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched, ChatID: "chat-1", Root: status(core.Failed()), RootFailureDetail: "worker lost"},
			status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		{name: "row cancelled", obs: launched(status(core.Cancelled())), status: core.CheckStatusCompleted, conclusion: ConclusionCancelled},
		{name: "paused past the grace", row: func(r *core.WorkflowCheckRun) { p := t0; r.PausedSince = &p },
			obs: launched(status(core.Paused())), now: t0.Add(PauseGrace), status: core.CheckStatusCompleted, conclusion: ConclusionFailure, title: "Reliant could not run this check"},
		// Firings that never launched.
		{name: "skipped", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventSkipped, EventDetail: "previous run is running"},
			status: core.CheckStatusCompleted, conclusion: ConclusionCancelled, title: "Not run: previous run is running"},
		{name: "superseded by a newer firing", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventSkipped, EventDetail: "superseded by a newer firing"},
			status: core.CheckStatusCompleted, conclusion: ConclusionCancelled, title: "Not run: superseded"},
		{name: "fire failed (engine error)", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventFailed, EventDetail: "trigger's daemon no longer exists"},
			status: core.CheckStatusCompleted, conclusion: ConclusionFailure, title: "Reliant could not run this check"},
		{name: "launched, chat deleted", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventLaunched},
			status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		{name: "firing deleted", obs: nil, status: core.CheckStatusCompleted, conclusion: ConclusionFailure},
		// Timeouts.
		{name: "queued past the timeout", obs: &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending}, now: t0.Add(time.Hour),
			status: core.CheckStatusCompleted, conclusion: ConclusionTimedOut, title: "Timed out"},
		{name: "running past the timeout", row: func(r *core.WorkflowCheckRun) { r.StartedAt = &started },
			obs: launched(status(core.Active())), now: t0.Add(2 * time.Hour), status: core.CheckStatusCompleted, conclusion: ConclusionTimedOut},
		{name: "an ending before the timeout stands after it", row: func(r *core.WorkflowCheckRun) { r.EndedStatus = core.TriggerRunCompleted },
			obs: launched(status(core.Completed())), now: t0.Add(2 * time.Hour), status: core.CheckStatusCompleted, conclusion: ConclusionSuccess},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := checkRow()
			if c.row != nil {
				c.row(row)
			}
			now := c.now
			if now.IsZero() {
				now = t0.Add(10 * time.Minute)
			}
			s, _ := derive(row, c.obs, now)
			require.Equal(t, c.status, s.Status)
			require.Equal(t, c.conclusion, s.Conclusion)
			require.NotContains(t, []string{"neutral", "skipped"}, s.Conclusion)
			require.NotEmpty(t, s.Title)
			require.NotEmpty(t, s.Summary)
			if c.title != "" {
				require.True(t, strings.HasPrefix(s.Title, c.title), "title %q", s.Title)
			}
		})
	}
}

func TestDerivePausedSinceTracksThePause(t *testing.T) {
	row := checkRow()
	row.StartedAt = &t0
	now := t0.Add(3 * time.Minute)

	_, since := derive(row, launched(status(core.Paused())), now)
	require.NotNil(t, since)
	require.Equal(t, now, *since, "the first paused sighting starts the grace")

	row.PausedSince = since
	_, kept := derive(row, launched(status(core.Paused())), now.Add(time.Minute))
	require.Equal(t, now, *kept, "a later sighting keeps when it started")

	_, cleared := derive(row, launched(status(core.Active())), now.Add(2*time.Minute))
	require.Nil(t, cleared, "a resumed run clears it")
}

func TestOnlyTheRunsOwnEndingUsesItsOutput(t *testing.T) {
	ended, _ := derive(&core.WorkflowCheckRun{EndedStatus: core.TriggerRunCompleted, CreatedAt: t0, Timeout: time.Hour}, nil, t0)
	require.True(t, ended.RunOutput)
	skipped, _ := derive(checkRow(), &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventSkipped}, t0)
	require.False(t, skipped.RunOutput)
	timedOut, _ := derive(checkRow(), &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending}, t0.Add(time.Hour))
	require.False(t, timedOut.RunOutput)
}
