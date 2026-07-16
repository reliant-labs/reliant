// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ResetExpiredWorkflow resets an expired (timed-out) workflow execution back to the
// last WorkflowTaskCompleted event. This restores the workflow to its pause point
// so it can be resumed with a fresh signal.
//
// The function:
// 1. Walks the workflow history to find the last WorkflowTaskCompleted event
// 2. Calls ResetWorkflowExecution targeting that event
// 3. Excludes signal reapply so pause/resume signals from the old run don't replay
//
// After reset, the caller must send a SignalResume to the new run to unblock
// the workflow from its Receive() loop.
func ResetExpiredWorkflow(ctx context.Context, tempClient client.Client, workflowID, runID string) (newRunID string, err error) {
	resetEventID, err := findLastWorkflowTaskCompleted(ctx, tempClient, workflowID, runID)
	if err != nil {
		return "", fmt.Errorf("failed to find reset point in workflow history: %w", err)
	}

	logging.Info("[ResetExpiredWorkflow] Resetting workflow",
		"workflowID", workflowID,
		"runID", runID,
		"resetEventID", resetEventID,
	)

	resp, err := tempClient.ResetWorkflowExecution(ctx, &workflowservice.ResetWorkflowExecutionRequest{
		Namespace: TemporalNamespace,
		WorkflowExecution: &commonpb.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
		WorkflowTaskFinishEventId: resetEventID,
		Reason:                    "Resuming expired paused workflow",
		ResetReapplyExcludeTypes: []enumspb.ResetReapplyExcludeType{
			enumspb.RESET_REAPPLY_EXCLUDE_TYPE_SIGNAL, // Don't reapply pause/resume signals
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to reset workflow execution: %w", err)
	}

	logging.Info("[ResetExpiredWorkflow] Workflow reset successfully",
		"workflowID", workflowID,
		"oldRunID", runID,
		"newRunID", resp.RunId,
		"resetEventID", resetEventID,
	)

	return resp.RunId, nil
}

// ResetInterruptedWorkflow resets a CLOSED-but-replayable execution (Failed,
// Terminated, or TimedOut) back to a replayable point so a new run can continue
// where the interrupted one left off. Unlike ResetExpiredWorkflow — which always
// targets the last WorkflowTaskCompleted (correct for a workflow parked in its
// resume Receive) — this picks the reset point from the closure shape:
//
//   - FAILED because a tail activity failed (the classic transient interruption:
//     OOM, worker kill, rate-limit): reset to the last WorkflowTaskCompleted
//     BEFORE that activity was scheduled, so the activity RE-EXECUTES fresh on
//     the new run (activities are not replayed; only workflow commands are). This
//     is what lets a transient failure recover cleanly.
//   - Otherwise (TERMINATED / TIMED_OUT / FAILED not caused by a tail activity):
//     reset to the last WorkflowTaskCompleted — the park / last-decision point.
//     A parked workflow re-parks and the caller's resume signal unblocks it; a
//     stalled workflow re-issues its pending work.
//
// Either way, replay rebuilds the ENTIRE goroutine stack — including any nested
// inline sub-workflow loops (e.g. get-it-right's review loop, which runs inline
// in the parent's single Temporal execution) with their iteration counters,
// prev-iteration outputs, forked reviewer threads, and join state intact. That
// precision is the whole reason this path is preferred over a fresh
// restart-with-flat-checkpoint, which cannot express a nested position.
//
// Old pause/resume signals are excluded from reapply so they don't re-pause the
// new run; the caller sends a fresh resume signal afterward.
func ResetInterruptedWorkflow(ctx context.Context, tempClient client.Client, workflowID, runID string, status enumspb.WorkflowExecutionStatus) (newRunID string, err error) {
	resetEventID, err := findResumeResetPoint(ctx, tempClient, workflowID, runID, status)
	if err != nil {
		return "", fmt.Errorf("failed to find reset point in workflow history: %w", err)
	}

	logging.Info("[ResetInterruptedWorkflow] Resetting interrupted workflow",
		"workflowID", workflowID,
		"runID", runID,
		"status", status.String(),
		"resetEventID", resetEventID,
	)

	resp, err := tempClient.ResetWorkflowExecution(ctx, &workflowservice.ResetWorkflowExecutionRequest{
		Namespace: TemporalNamespace,
		WorkflowExecution: &commonpb.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
		WorkflowTaskFinishEventId: resetEventID,
		Reason:                    "Resuming interrupted workflow (reset-and-replay)",
		ResetReapplyExcludeTypes: []enumspb.ResetReapplyExcludeType{
			enumspb.RESET_REAPPLY_EXCLUDE_TYPE_SIGNAL, // Don't reapply stale pause/resume signals
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to reset workflow execution: %w", err)
	}

	logging.Info("[ResetInterruptedWorkflow] Workflow reset successfully",
		"workflowID", workflowID,
		"oldRunID", runID,
		"newRunID", resp.RunId,
		"resetEventID", resetEventID,
	)

	return resp.RunId, nil
}

// findResumeResetPoint walks history once and returns the WorkflowTaskCompleted
// EventId to reset to for ResetInterruptedWorkflow. See that function's doc for
// the strategy. Returns an error only when there is no WorkflowTaskCompleted at
// all (the workflow closed before completing its first workflow task — nothing
// replayable to resume into).
//
// "Reset before the failing activity" is applied ONLY when the run FAILED and
// the failing activity is in the tail (no successful activity completion after
// it). That guard matters: a TERMINATED run may carry an OLD activity failure
// from a prior self-pause followed by lots of forward progress — resetting
// before that stale failure would silently discard the progress, so terminated
// runs always use the safe last-WorkflowTaskCompleted point.
func findResumeResetPoint(ctx context.Context, tempClient client.Client, workflowID, runID string, status enumspb.WorkflowExecutionStatus) (int64, error) {
	iter := tempClient.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	var (
		lastWFTCompleted int64
		// scheduledToWFT maps an ActivityTaskScheduled event's ID to the last
		// WorkflowTaskCompleted before it (the decision that scheduled it).
		scheduledToWFT = map[int64]int64{}
		// Position (event ID) of the last successful activity completion and the
		// last terminal activity failure, plus the reset point that would
		// re-issue that failing activity.
		lastActivityCompletedAt int64
		lastActivityFailedAt    int64
		failingActivityResetTo  int64
	)

	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return 0, fmt.Errorf("failed to iterate workflow history: %w", err)
		}

		switch event.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED:
			lastWFTCompleted = event.GetEventId()
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			scheduledToWFT[event.GetEventId()] = lastWFTCompleted
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED:
			if event.GetEventId() > lastActivityCompletedAt {
				lastActivityCompletedAt = event.GetEventId()
			}
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED:
			lastActivityFailedAt = event.GetEventId()
			failingActivityResetTo = scheduledToWFT[event.GetActivityTaskFailedEventAttributes().GetScheduledEventId()]
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_TIMED_OUT:
			lastActivityFailedAt = event.GetEventId()
			failingActivityResetTo = scheduledToWFT[event.GetActivityTaskTimedOutEventAttributes().GetScheduledEventId()]
		}
	}

	if lastWFTCompleted == 0 {
		return 0, fmt.Errorf("no WorkflowTaskCompleted event found in history for workflow %s (run %s)", workflowID, runID)
	}

	// Only re-run the failing activity when the run FAILED on a tail activity
	// (the failure is the last activity outcome and produced a valid pre-schedule
	// reset point). Terminated/timed-out runs and code-level failures take the
	// safe last-decision point.
	if status == enumspb.WORKFLOW_EXECUTION_STATUS_FAILED &&
		lastActivityFailedAt > lastActivityCompletedAt &&
		failingActivityResetTo > 0 {
		return failingActivityResetTo, nil
	}

	return lastWFTCompleted, nil
}

// findLastWorkflowTaskCompleted walks the full workflow history and returns the
// EventId of the last WorkflowTaskCompleted event. This is the point where the
// workflow entered its Receive() block waiting for a resume signal — the ideal
// reset target for expired paused workflows.
func findLastWorkflowTaskCompleted(ctx context.Context, tempClient client.Client, workflowID, runID string) (int64, error) {
	iter := tempClient.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	var lastWorkflowTaskCompletedID int64

	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return 0, fmt.Errorf("failed to iterate workflow history: %w", err)
		}

		if event.EventType == enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED {
			lastWorkflowTaskCompletedID = event.EventId
		}
	}

	if lastWorkflowTaskCompletedID == 0 {
		return 0, fmt.Errorf("no WorkflowTaskCompleted event found in history for workflow %s (run %s)", workflowID, runID)
	}

	return lastWorkflowTaskCompletedID, nil
}
