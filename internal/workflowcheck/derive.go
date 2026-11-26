// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Conclusions the engine writes. Never neutral or skipped: a required check
// counts both as passing, so neither may stand for a run that did not pass.
const (
	ConclusionSuccess   = "success"
	ConclusionFailure   = "failure"
	ConclusionCancelled = "cancelled"
	ConclusionTimedOut  = "timed_out"
)

// PauseGrace is how long a paused run keeps its check in_progress. An
// unattended run pauses for a usage limit or a machine and usually resumes on
// its own; past this, the check concludes failure, so a run that is not coming
// back never holds a required check open until its timeout.
const PauseGrace = 15 * time.Minute

// verdictFailure is the Node.outcome that fails a run's verdict
// (model.OutcomeFailure).
const verdictFailure = "failure"

// verdictSkipped is the Node.outcome a run declares when it chose not to do
// its work this time (model.OutcomeSkipped). Such a run verified nothing, so
// its check concludes cancelled: never a passing conclusion, and not a
// failure either.
const verdictSkipped = "skipped"

// state is what a check should say on GitHub now, derived from its firing and
// run alone. It is recomputed on every sync, so a sync that is lost, retried
// or run twice converges on the same answer.
type state struct {
	Status     string // core.CheckStatus*
	Conclusion string // when Status is completed
	// Title and Summary are the engine's own words for this state. The run's
	// output replaces them when RunOutput is set and the run evaluated one.
	Title   string
	Summary string
	// RunOutput is set when the run ended by itself, so its check output (if
	// it evaluated one) is what the check shows.
	RunOutput bool
}

// final reports whether the state can no longer change.
func (s state) final() bool { return s.Status == core.CheckStatusCompleted }

// derive is the check's state for row, given what is observed of its firing
// and run (obs nil: the firing is gone), at now. pausedSince is the value
// row.PausedSince should hold after this observation.
//
// The order is the order of authority: the run's own report of its end; then
// the firing's outcome, for a firing that never launched; then the trigger's
// record of its run's end; then the run's workflow row. The timeout applies
// only to a check none of them concluded.
func derive(row *core.WorkflowCheckRun, obs *core.WorkflowCheckObservation, now time.Time) (s state, pausedSince *time.Time) {
	s, pausedSince = observed(row, obs, now)
	if !s.final() && row.Timeout > 0 && !now.Before(row.CreatedAt.Add(row.Timeout)) {
		return state{
			Status: core.CheckStatusCompleted, Conclusion: ConclusionTimedOut,
			Title:   fmt.Sprintf("Timed out: the run did not finish within %s", row.Timeout),
			Summary: fmt.Sprintf("The check's timeout (%s, from the firing) passed while the run was %s. Re-run the check to try again.", row.Timeout, describe(s)),
		}, nil
	}
	return s, pausedSince
}

func observed(row *core.WorkflowCheckRun, obs *core.WorkflowCheckObservation, now time.Time) (state, *time.Time) {
	if row.EndedStatus != "" {
		return ended(row.EndedStatus, row.Verdict, ""), nil
	}
	if obs == nil {
		return couldNotRun("its firing record was deleted"), nil
	}
	switch obs.EventOutcome {
	case core.TriggerEventSkipped:
		return state{
			Status: core.CheckStatusCompleted, Conclusion: ConclusionCancelled,
			Title:   "Not run: " + firstLine(obs.EventDetail, "the firing was skipped"),
			Summary: "The trigger fired but did not start a run: " + orDefault(obs.EventDetail, "skipped") + ".",
		}, nil
	case core.TriggerEventFailed:
		return couldNotRun(orDefault(obs.EventDetail, "the run could not be started")), nil
	case core.TriggerEventPending:
		return queued("Waiting to start: the run is queued for capacity or a machine."), nil
	}
	// Launched.
	switch obs.RunStatus {
	case core.TriggerRunCompleted, core.TriggerRunFailed, core.TriggerRunCancelled:
		// The trigger records a declared failure as run_status failed, but a
		// declared skip as completed with run_outcome skipped.
		verdict := ""
		if obs.RunOutcome == core.RunOutcomeSkipped {
			verdict = verdictSkipped
		}
		return ended(obs.RunStatus, verdict, ""), nil
	}
	if obs.ChatID == "" {
		return couldNotRun("its run was deleted"), nil
	}
	if obs.Root == nil {
		return queued("Starting the run."), nil
	}
	root := *obs.Root
	switch root.State {
	case core.WorkflowStatePending:
		return queued("Starting the run."), nil
	case core.WorkflowStateActive:
		if row.StartedAt == nil {
			return queued("Waiting for the run's machine."), nil
		}
		return inProgress("Running", "The run is in progress."), nil
	case core.WorkflowStateStopped:
		switch root.StopReason {
		case core.StopReasonCompleted:
			return ended(core.TriggerRunCompleted, obs.RootVerdict, ""), nil
		case core.StopReasonFailed:
			return ended(core.TriggerRunFailed, "", obs.RootFailureDetail), nil
		case core.StopReasonCancelled:
			return ended(core.TriggerRunCancelled, "", ""), nil
		case core.StopReasonPaused:
			since := now
			if row.PausedSince != nil {
				since = *row.PausedSince
			}
			if now.Sub(since) >= PauseGrace {
				return couldNotRun(fmt.Sprintf("the run paused and did not resume within %s", PauseGrace)), nil
			}
			return inProgress("Paused", "The run paused (a usage limit, a machine, or a step that needs attention) and usually resumes by itself. "+
				fmt.Sprintf("If it has not resumed within %s, this check fails.", PauseGrace)), &since
		}
	}
	return queued("Starting the run."), nil
}

// ended is the state of a run that ended with status (a core.TriggerRun*
// value) and verdict.
func ended(status, verdict, detail string) state {
	s := state{Status: core.CheckStatusCompleted, RunOutput: true}
	switch {
	case status == core.TriggerRunCompleted && verdict == verdictSkipped:
		s.Conclusion, s.Title, s.Summary = ConclusionCancelled, "Skipped: the run skipped its work",
			"The run completed into a node that declares outcome: skipped: it chose not to do its work this time, so it verified nothing. Re-run the check to try again."
	case status == core.TriggerRunCompleted && verdict != verdictFailure:
		s.Conclusion, s.Title, s.Summary = ConclusionSuccess, "Passed", "The run completed."
	case status == core.TriggerRunCompleted:
		s.Conclusion, s.Title, s.Summary = ConclusionFailure, "Failed", "The run completed with a failure outcome."
	case status == core.TriggerRunCancelled:
		s.Conclusion, s.Title, s.Summary = ConclusionCancelled, "Cancelled", "The run was cancelled before it finished."
	default:
		s.Conclusion, s.Title, s.Summary = ConclusionFailure, "Failed: the run failed", "The run failed."
		if detail != "" {
			s.Summary += "\n\n" + detail
		}
	}
	return s
}

// couldNotRun is an engine-side failure: the check could not be run, which is
// a failure (never neutral), titled so a reader knows a re-run may pass.
func couldNotRun(why string) state {
	return state{
		Status: core.CheckStatusCompleted, Conclusion: ConclusionFailure,
		Title:   "Reliant could not run this check: re-run it",
		Summary: "Reliant could not run this check: " + why + ".",
	}
}

func queued(summary string) state {
	return state{Status: core.CheckStatusQueued, Title: "Queued", Summary: summary}
}

func inProgress(title, summary string) state {
	return state{Status: core.CheckStatusInProgress, Title: title, Summary: summary}
}

// describe names a non-final state for a timeout's summary.
func describe(s state) string {
	switch {
	case s.Status == core.CheckStatusQueued:
		return "still queued"
	case s.Title == "Paused":
		return "paused"
	default:
		return "still running"
	}
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s, 200)
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}
