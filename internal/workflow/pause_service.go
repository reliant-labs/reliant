// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ErrWorkflowNotFound is returned when a workflow cannot be found in Temporal.
var ErrWorkflowNotFound = errors.New("workflow not found")

// PauseService bridges gRPC pause/resume requests to Temporal signals.
// It sends signal.pause / signal.resume to the workflow and updates the
// workflow status in the database so the UI reflects the current state.
type PauseService struct {
	temporalClient client.Client
	database       db.Repository
}

// NewPauseService creates a PauseService with a Temporal client for signaling
// and a database handle for status updates.
func NewPauseService(temporalClient client.Client, database db.Repository) *PauseService {
	return &PauseService{
		temporalClient: temporalClient,
		database:       database,
	}
}

// PauseWorkflow sends a pause signal to the Temporal workflow and updates the
// DB status to paused. The workflow cooperatively pauses at the next step boundary.
// If the workflow has already completed, it reconciles the DB status and returns nil
// since the user's intent (stop the workflow) is already satisfied.
func (ps *PauseService) PauseWorkflow(ctx context.Context, workflowID, chatID, reason string) error {
	logging.Info("[PauseService] Sending pause signal",
		"workflowID", workflowID,
		"chatID", chatID,
		"reason", reason,
	)

	// Send signal.pause to the Temporal workflow
	err := ps.temporalClient.SignalWorkflow(ctx, workflowID, "", SignalPause, nil)
	if err != nil {
		if isWorkflowAlreadyDoneErr(err) {
			// Workflow already finished — reconcile DB status and return success
			logging.Warn("[PauseService] Workflow already completed, reconciling DB status",
				"workflowID", workflowID,
				"signalError", err,
			)
			ps.reconcileTerminalStatus(ctx, workflowID)
			return nil
		}
		return fmt.Errorf("failed to send pause signal: %w", err)
	}

	// Update workflow status in DB so the UI shows "paused"
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusPaused); err != nil {
		logging.Error("[PauseService] Failed to update workflow status to paused",
			"workflowID", workflowID,
			"error", err,
		)
		return fmt.Errorf("failed to update workflow status: %w", err)
	}

	// Pause all remaining running workflows for this chat (child threads, agent workflows, etc.)
	// so the chats_with_activity view correctly reports the chat as paused.
	if chatID != "" {
		if err := ps.database.PauseRunningWorkflowsByChat(ctx, chatID); err != nil {
			logging.Error("[PauseService] Failed to pause child workflows",
				"workflowID", workflowID,
				"chatID", chatID,
				"error", err,
			)
			// Don't fail — the root workflow is already paused
		}
	}

	logging.Info("[PauseService] Workflow paused successfully",
		"workflowID", workflowID,
	)
	return nil
}

// isWorkflowAlreadyDoneErr checks if a Temporal error indicates the workflow
// has already completed or cannot be found. Uses the same pattern as
// SignalWithRecovery.
func isWorkflowAlreadyDoneErr(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "NotFound") ||
		strings.Contains(errStr, "already completed") ||
		strings.Contains(errStr, "WorkflowNotFound")
}

// reconcileTerminalStatus queries Temporal for the workflow's actual status
// and updates the DB to match. If Temporal can't be reached or the workflow
// is gone, it defaults to completed.
func (ps *PauseService) reconcileTerminalStatus(ctx context.Context, workflowID string) {
	status := db.WorkflowStatusCompleted // default if we can't determine

	descResp, err := ps.temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err == nil && descResp != nil && descResp.WorkflowExecutionInfo != nil {
		switch descResp.WorkflowExecutionInfo.Status {
		case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
			status = db.WorkflowStatusCompleted
		case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
			status = db.WorkflowStatusFailed
		case enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
			status = db.WorkflowStatusCancelled
		default:
			status = db.WorkflowStatusCompleted
		}
	}

	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, status); err != nil {
		logging.Error("[PauseService] Failed to reconcile workflow status",
			"workflowID", workflowID,
			"targetStatus", status,
			"error", err,
		)
	}
}

// SignalWithRecovery sends a signal to a workflow. If the Temporal execution has
// expired (14-day timeout), it resets the workflow to its last decision point and
// re-sends the signal on the new run. This is the same recovery mechanism used
// by pause/resume.
func (ps *PauseService) SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}) error {
	err := ps.temporalClient.SignalWorkflow(ctx, workflowID, "", signalName, signalData)
	if err != nil {
		if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "NotFound") {
			return fmt.Errorf("failed to send signal %s: %w", signalName, err)
		}
		// Expired — reset and re-signal
		newRunID, resetErr := ResetExpiredWorkflow(ctx, ps.temporalClient, workflowID, "")
		if resetErr != nil {
			return fmt.Errorf("failed to reset expired workflow for signal %s: %w", signalName, resetErr)
		}
		if err := ps.temporalClient.SignalWorkflow(ctx, workflowID, newRunID, signalName, signalData); err != nil {
			return fmt.Errorf("failed to send signal %s after reset: %w", signalName, err)
		}
	}
	return nil
}

// ResumeWorkflow resumes a paused workflow. For live Temporal executions it sends
// a signal.resume. For expired executions (Temporal timed out after ~14 days), it
// resets the workflow to the pause point and then signals resume on the new run.
func (ps *PauseService) ResumeWorkflow(ctx context.Context, workflowID, chatID string) error {
	logging.Info("[PauseService] Resuming workflow",
		"workflowID", workflowID,
		"chatID", chatID,
	)

	err := ps.SignalWithRecovery(ctx, workflowID, SignalResume, nil)
	if err != nil {
		// Check if the error is because the workflow is truly not found (not just expired)
		if strings.Contains(err.Error(), "failed to reset expired workflow") {
			// Reset failed — check DB to give a better error
			wf, dbErr := ps.database.GetWorkflow(ctx, workflowID)
			if dbErr != nil {
				return fmt.Errorf("%w: %s", ErrWorkflowNotFound, workflowID)
			}
			if wf.Status != db.WorkflowStatusExpired && wf.Status != db.WorkflowStatusPaused {
				return fmt.Errorf("%w: %s (status: %d)", ErrWorkflowNotFound, workflowID, wf.Status)
			}
		}
		return fmt.Errorf("failed to resume workflow: %w", err)
	}

	// Signal sent successfully — update DB status to running
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning); err != nil {
		logging.Error("[PauseService] Failed to update workflow status to running",
			"workflowID", workflowID,
			"error", err,
		)
		// Don't fail — the workflow is already resumed in Temporal
	}

	// Resume all paused child workflows for this chat
	if chatID != "" {
		if err := ps.database.ResumeWorkflowsByChat(ctx, chatID); err != nil {
			logging.Error("[PauseService] Failed to resume child workflows",
				"workflowID", workflowID,
				"chatID", chatID,
				"error", err,
			)
			// Don't fail — the root workflow is already resumed
		}
	}

	logging.Info("[PauseService] Workflow resumed successfully",
		"workflowID", workflowID,
	)
	return nil
}

// ResumeExpiredWorkflow resets an expired (timed-out) workflow execution back to
// its pause point and sends a resume signal on the new run. Returns the new run ID.
func (ps *PauseService) ResumeExpiredWorkflow(ctx context.Context, workflowID, chatID string) (string, error) {
	logging.Info("[PauseService] Resetting expired workflow",
		"workflowID", workflowID,
		"chatID", chatID,
	)

	// Reset the workflow to the last WorkflowTaskCompleted event (the pause point).
	// Pass empty runID so Temporal uses the latest run.
	newRunID, err := ResetExpiredWorkflow(ctx, ps.temporalClient, workflowID, "")
	if err != nil {
		return "", fmt.Errorf("failed to reset expired workflow: %w", err)
	}

	// Send resume signal to the new run so it unblocks from its Receive() loop
	err = ps.temporalClient.SignalWorkflow(ctx, workflowID, newRunID, SignalResume, nil)
	if err != nil {
		return "", fmt.Errorf("failed to send resume signal after reset: %w", err)
	}

	// Update DB status to running
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning); err != nil {
		logging.Error("[PauseService] Failed to update workflow status to running after reset",
			"workflowID", workflowID,
			"error", err,
		)
		// Don't fail — the workflow is already resumed
	}

	logging.Info("[PauseService] Expired workflow reset and resumed",
		"workflowID", workflowID,
		"newRunID", newRunID,
	)
	return newRunID, nil
}
