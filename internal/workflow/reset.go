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
