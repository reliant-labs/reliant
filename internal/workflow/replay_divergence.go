// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"errors"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

// ErrReplayDiverged reports that a run's recorded history can no longer be
// replayed by this code: its last workflow task failed as non-deterministic
// (TMPRL1100), which is what a deploy that changes the workflow's command
// sequence does to a run that was in flight.
//
// Reset-and-replay cannot recover such a run. A reset replays the history from
// event 1 with the current code, so the new run diverges at the same event and
// wedges exactly like the old one; reset points before the divergence would
// discard the conversation's progress. The caller falls back to the coarse
// fresh restart, which runs the current code against an EMPTY history and
// re-enters at the checkpointed position.
var ErrReplayDiverged = errors.New("workflow history no longer replays on the current code")

// IsReplayDivergence reports whether a workflow-task failure cause means the
// worker could not replay the recorded history.
func IsReplayDivergence(cause enumspb.WorkflowTaskFailedCause) bool {
	return cause == enumspb.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR
}

// WorkflowTaskOutcome is how a run's most recent workflow task ended, read
// from its recorded history.
type WorkflowTaskOutcome struct {
	// EventType is WORKFLOW_TASK_COMPLETED, _FAILED or _TIMED_OUT, or
	// UNSPECIFIED when the scanned tail held no workflow-task outcome.
	EventType enumspb.EventType
	// Cause is the failure cause when EventType is WORKFLOW_TASK_FAILED.
	Cause enumspb.WorkflowTaskFailedCause
	// EventID is the outcome event's id.
	EventID int64
	// CancelRequested reports a cancel request recorded since the last
	// workflow task that completed: one the run has not acted on yet.
	CancelRequested bool
}

// ReplayDiverged reports that the run's latest workflow task failed because
// the worker's code no longer replays its history. See ErrReplayDiverged.
func (o WorkflowTaskOutcome) ReplayDiverged() bool {
	return o.EventType == enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED && IsReplayDivergence(o.Cause)
}

// FailsDeterministically reports that the latest workflow task failed in a
// way every retry of it repeats with the code the workers run now: a replay
// divergence, or a panic in the workflow code itself. Such a run makes no
// progress until something outside it changes. A TIMED OUT task is not one —
// that is a slow or overloaded worker, which a retry can get past.
func (o WorkflowTaskOutcome) FailsDeterministically() bool {
	if o.EventType != enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED {
		return false
	}
	return IsReplayDivergence(o.Cause) ||
		o.Cause == enumspb.WORKFLOW_TASK_FAILED_CAUSE_WORKFLOW_WORKER_UNHANDLED_FAILURE
}

// historyReverseReader is the one Temporal call LatestWorkflowTaskOutcome
// makes. client.Client.WorkflowService() satisfies it.
type historyReverseReader interface {
	GetWorkflowExecutionHistoryReverse(ctx context.Context, in *workflowservice.GetWorkflowExecutionHistoryReverseRequest, opts ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryReverseResponse, error)
}

const (
	// outcomePageSize and outcomeScanLimit bound the backward read. The
	// outcome is normally within a few events of the end; signals that piled
	// up behind a wedged task (each user resend adds three) are what push it
	// further back.
	outcomePageSize  = 100
	outcomeScanLimit = 2000
)

// LatestWorkflowTaskOutcome reads a run's history BACKWARD from its newest
// event to find how its most recent workflow task ended, and whether a cancel
// is pending. It stops at the last completed workflow task, so it reads only
// the tail no matter how long the history is (prod's wedged runs held 11k and
// 26k events).
//
// Temporal records only the FIRST failure of a run of retried workflow tasks;
// the retries themselves are transient and leave no events. So the outcome
// found here is the failure the retries keep repeating, and a pending task's
// attempt count (DescribeWorkflowExecution) is how many times it has.
func LatestWorkflowTaskOutcome(ctx context.Context, reader historyReverseReader, namespace, workflowID, runID string) (WorkflowTaskOutcome, error) {
	var (
		outcome WorkflowTaskOutcome
		token   []byte
		scanned int
	)
	for scanned < outcomeScanLimit {
		resp, err := reader.GetWorkflowExecutionHistoryReverse(ctx, &workflowservice.GetWorkflowExecutionHistoryReverseRequest{
			Namespace:       namespace,
			Execution:       &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: runID},
			MaximumPageSize: outcomePageSize,
			NextPageToken:   token,
		})
		if err != nil {
			return WorkflowTaskOutcome{}, fmt.Errorf("read history of %s backward: %w", workflowID, err)
		}
		for _, event := range resp.GetHistory().GetEvents() {
			scanned++
			switch event.GetEventType() {
			case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCEL_REQUESTED:
				outcome.CancelRequested = true
			case enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED:
				if outcome.EventType == enumspb.EVENT_TYPE_UNSPECIFIED {
					outcome.EventType = event.GetEventType()
					outcome.Cause = event.GetWorkflowTaskFailedEventAttributes().GetCause()
					outcome.EventID = event.GetEventId()
				}
			case enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT:
				if outcome.EventType == enumspb.EVENT_TYPE_UNSPECIFIED {
					outcome.EventType = event.GetEventType()
					outcome.EventID = event.GetEventId()
				}
			case enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED:
				if outcome.EventType == enumspb.EVENT_TYPE_UNSPECIFIED {
					outcome.EventType = event.GetEventType()
					outcome.EventID = event.GetEventId()
				}
				// Everything before the last completed task was acted on.
				return outcome, nil
			}
		}
		token = resp.GetNextPageToken()
		if len(token) == 0 {
			break
		}
	}
	return outcome, nil
}
