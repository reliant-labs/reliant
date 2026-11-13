// Copyright (c) 2025 Reliant Labs

package orgmove

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// WorkflowName is the registered name of ProjectOrgMoveWorkflow.
const WorkflowName = "ProjectOrgMove"

// The registered names of the move's activities (Service's methods).
const (
	PrepareActivityName        = "ProjectOrgMove.Prepare"
	CommitActivityName         = "ProjectOrgMove.Commit"
	AbortActivityName          = "ProjectOrgMove.Abort"
	RecordActivityName         = "ProjectOrgMove.Record"
	ResumeTriggersActivityName = "ProjectOrgMove.ResumeTriggers"
)

// WorkflowID is the Temporal workflow id of a move: one workflow per move
// row, so starting it twice is starting it once.
func WorkflowID(moveID string) string { return "project-org-move-" + moveID }

// beforeCommit is how long the steps before the commit may keep failing
// (control-plane down, the database unreachable) before the move gives up and
// aborts, rather than holding the project's secret access revoked.
const beforeCommit = 30 * time.Minute

// rollForward bounds the steps that must eventually succeed: compensation
// before the commit, and the activity events and trigger re-enables after it.
// Past it the move stays where it is (aborting, or moved) for an operator,
// visible in the ledger; nothing is half-applied, because each step is one
// transaction or one idempotent call.
const rollForward = 7 * 24 * time.Hour

func activityOptions(scheduleToClose time.Duration) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    2 * time.Minute,
		ScheduleToCloseTimeout: scheduleToClose,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    5 * time.Minute,
		},
	}
}

// ProjectOrgMoveWorkflow drives one move (control-plane
// docs/design/org-project-move.md §3.1): Prepare, Commit, then Record and
// ResumeTriggers; or, when Prepare or Commit says to stop or keeps failing
// for beforeCommit, Abort. A Commit whose answer was lost may have committed:
// Abort then reports so, and the move rolls forward instead.
func ProjectOrgMoveWorkflow(ctx workflow.Context, moveID string) error {
	before := workflow.WithActivityOptions(ctx, activityOptions(beforeCommit))
	forward := workflow.WithActivityOptions(ctx, activityOptions(rollForward))

	var out Outcome
	err := workflow.ExecuteActivity(before, PrepareActivityName, moveID).Get(before, &out)
	if err == nil && !out.Abort {
		err = workflow.ExecuteActivity(before, CommitActivityName, moveID).Get(before, &out)
	}
	if err != nil || out.Abort {
		detail := out.Detail
		if err != nil {
			detail = "the move could not be prepared or committed in time"
		}
		var aborted bool
		if err := workflow.ExecuteActivity(forward, AbortActivityName, moveID, detail).Get(forward, &aborted); err != nil {
			return err
		}
		if aborted {
			return nil
		}
	}
	if err := workflow.ExecuteActivity(forward, RecordActivityName, moveID).Get(forward, nil); err != nil {
		return err
	}
	return workflow.ExecuteActivity(forward, ResumeTriggersActivityName, moveID).Get(forward, nil)
}
