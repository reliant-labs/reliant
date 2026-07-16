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

// ErrNoReplayableHistory is returned by ResumeInterruptedWorkflow when the
// interrupted workflow has no replayable Temporal history — true data loss: the
// execution is gone from Temporal (past retention, server wipe, or never
// recorded). The caller must fall back to the coarse fresh-restart-with-
// checkpoint (ghost) path.
var ErrNoReplayableHistory = errors.New("no replayable temporal history")

// ErrResetAttemptsExhausted is returned when the reset-attempt guard has bounded
// further reset-and-replay attempts for this workflow (it kept re-failing at the
// same point without forward progress — a deterministic error or a replay
// wedge). The caller falls back to the coarse fresh-restart-with-checkpoint,
// which runs current code with NO old history to replay — the correct recovery
// for exactly that failure class.
var ErrResetAttemptsExhausted = errors.New("reset attempts exhausted")

// PauseService bridges gRPC pause/resume requests to Temporal signals.
// It sends signal.pause / signal.resume to the workflow and updates the
// workflow status in the database so the UI reflects the current state.
type PauseService struct {
	temporalClient client.Client
	database       db.Repository

	// resetGuard bounds reset-and-replay attempts so a deterministically-failing
	// workflow is not reset forever. Optional (nil = always allow); shared with
	// the reconciler via SetResetGuard so both resetters count against one bound.
	resetGuard *ResetAttemptGuard
}

// NewPauseService creates a PauseService with a Temporal client for signaling
// and a database handle for status updates.
func NewPauseService(temporalClient client.Client, database db.Repository) *PauseService {
	return &PauseService{
		temporalClient: temporalClient,
		database:       database,
	}
}

// SetResetGuard installs the shared reset-attempt guard (see ResetAttemptGuard).
func (ps *PauseService) SetResetGuard(g *ResetAttemptGuard) {
	ps.resetGuard = g
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
			// Workflow already finished — reconcile DB status and return success.
			// reconcileTerminalStatus also cascades completion to child workflows.
			logging.Warn("[PauseService] Workflow already completed, reconciling DB status",
				"workflowID", workflowID,
				"chatID", chatID,
				"signalError", err,
			)
			ps.reconcileTerminalStatus(ctx, workflowID)
			// Also bulk-pause any remaining running workflows for this chat
			// (covers children that aren't direct descendants of this workflow).
			if chatID != "" {
				if pErr := ps.database.PauseRunningWorkflowsByChat(ctx, chatID); pErr != nil {
					logging.Error("[PauseService] Failed to pause remaining workflows after reconciliation",
						"workflowID", workflowID,
						"chatID", chatID,
						"error", pErr,
					)
				}
			}
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
		case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
			enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
			// TERMINATED = system/operator kill → Failed (resumable at
			// position). Only CANCELED (user cancel) maps to Cancelled.
			status = db.WorkflowStatusFailed
		case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
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
		return
	}

	// Cascade completion to child workflows. When a root workflow's Temporal
	// execution has expired, children's status notifications were likely lost
	// too — leaving them stuck at running/paused and the chat permanently "active".
	if err := ps.database.CompleteChildWorkflows(ctx, workflowID); err != nil {
		logging.Error("[PauseService] Failed to cascade completion to child workflows",
			"workflowID", workflowID,
			"error", err,
		)
	}
}

// SignalWithRecovery sends a signal to a workflow, recovering a dead-but-
// replayable target so the signal still lands. If the live execution rejects the
// signal because it is closed:
//   - Failed / Terminated / TimedOut with replayable history: reset-and-replay
//     it into a new run (honoring the ResetAttemptGuard) and re-send the signal
//     on that run. Temporal buffers the signal, and because the receiving
//     channel name is deterministic in history (e.g. signal.question.<id>, whose
//     QuestionID comes from the replayed QuestionCreate activity), the replayed
//     run re-parks on the same channel and receives it. This is what lets a
//     resume (signal.resume) OR a question answer (signal.question.<id>) recover
//     a dead question-parked / pause-parked workflow PRECISELY — its nested
//     inline stack and any active sub-threads are rebuilt with it (one
//     execution).
//   - Not found / past-retention: the legacy expired-reset recovery.
//
// A guard-exhausted target surfaces the legacy "failed to reset expired
// workflow" error so callers (ResumeWorkflow) fall back to the coarse restart.
func (ps *PauseService) SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}) error {
	err := ps.temporalClient.SignalWorkflow(ctx, workflowID, "", signalName, signalData)
	if err == nil {
		return nil
	}

	// Closed-execution rejection: try reset-and-replay recovery for a replayable
	// interrupted (Failed / Terminated / TimedOut) run.
	if isWorkflowAlreadyDoneErr(err) {
		newRunID, resetErr := ps.resetInterruptedForResume(ctx, workflowID, "")
		switch {
		case resetErr == nil:
			if sigErr := ps.temporalClient.SignalWorkflow(ctx, workflowID, newRunID, signalName, signalData); sigErr != nil {
				return fmt.Errorf("failed to send signal %s after reset: %w", signalName, sigErr)
			}
			return nil
		case errors.Is(resetErr, ErrResetAttemptsExhausted):
			// Bounded — surface as the legacy expired-reset error so callers fall
			// back to the coarse restart.
			return fmt.Errorf("failed to reset expired workflow for signal %s: %w", signalName, resetErr)
		default:
			// ErrNoReplayableHistory (completed / not-found / not eligible): fall
			// through to the legacy path below.
		}
	}

	// Legacy: only a truly-gone (not found / past-retention) execution attempts
	// the expired reset; anything else surfaces the original signal error.
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "NotFound") {
		return fmt.Errorf("failed to send signal %s: %w", signalName, err)
	}
	newRunID, resetErr := ResetExpiredWorkflow(ctx, ps.temporalClient, workflowID, "")
	if resetErr != nil {
		return fmt.Errorf("failed to reset expired workflow for signal %s: %w", signalName, resetErr)
	}
	if err := ps.temporalClient.SignalWorkflow(ctx, workflowID, newRunID, signalName, signalData); err != nil {
		return fmt.Errorf("failed to send signal %s after reset: %w", signalName, err)
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

// ResumeInterruptedWorkflow resumes a workflow whose prior run ended in a closed
// non-cancel state (Failed / Terminated / TimedOut) via reset-and-replay: it
// resets the closed execution to a replayable point and sends a fresh resume
// signal on the new run. Because every sub-workflow runs INLINE in the root
// Temporal execution, replay rebuilds the entire (possibly nested) engine stack
// — so a run that died mid-nested-get-it-right resumes at the SAME review
// iteration (iteration counter + reviewer feedback preserved) instead of
// restarting the loop at zero. This is the precise alternative to the coarse
// fresh-restart-with-flat-checkpoint, which cannot express a nested position.
//
// Returns the new run ID on success, or a sentinel telling the caller to fall
// back to the coarse restart:
//   - ErrNoReplayableHistory: Temporal has no eligible execution to replay
//     (ghost / past retention / running / user-cancelled).
//   - ErrResetAttemptsExhausted: the bounded guard has given up on resetting
//     this workflow (it kept re-failing without forward progress).
func (ps *PauseService) ResumeInterruptedWorkflow(ctx context.Context, workflowID, chatID string) (string, error) {
	newRunID, err := ps.resetInterruptedForResume(ctx, workflowID, chatID)
	if err != nil {
		return "", err
	}

	// Send resume to the new run so a parked workflow unblocks from its Await.
	// Harmless for a non-parked run: the resume coordinator just advances the
	// epoch (no goroutine is waiting on it).
	if err := ps.temporalClient.SignalWorkflow(ctx, workflowID, newRunID, SignalResume, nil); err != nil {
		return "", fmt.Errorf("failed to send resume signal after reset: %w", err)
	}

	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning); err != nil {
		logging.Error("[PauseService] Failed to update workflow status to running after interrupted resume",
			"workflowID", workflowID,
			"error", err,
		)
		// Don't fail — the workflow is already resumed in Temporal.
	}

	logging.Info("[PauseService] Interrupted workflow reset-and-resumed",
		"workflowID", workflowID,
		"chatID", chatID,
		"newRunID", newRunID,
	)
	return newRunID, nil
}

// resetInterruptedForResume describes the workflow, verifies it is a closed
// replayable interrupted run (Failed / Terminated / TimedOut), checks the
// bounded reset guard, and resets it to a replayable point — returning the new
// run ID. The CALLER sends the appropriate wake signal (signal.resume for a
// pause-parked run, signal.question.<id> for a question-parked run) on the new
// run. Returns ErrNoReplayableHistory (ghost / not eligible) or
// ErrResetAttemptsExhausted (guard gave up) for the caller to fall back on.
func (ps *PauseService) resetInterruptedForResume(ctx context.Context, workflowID, chatID string) (string, error) {
	desc, err := ps.temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		if isWorkflowAlreadyDoneErr(err) { // includes not-found
			return "", ErrNoReplayableHistory
		}
		return "", fmt.Errorf("failed to describe interrupted workflow: %w", err)
	}
	info := desc.GetWorkflowExecutionInfo()
	if info == nil || info.GetExecution() == nil {
		return "", ErrNoReplayableHistory
	}

	// Only closed, replayable, non-user-cancel states are eligible. RUNNING is
	// handled by the live-signal / ghost paths; CANCELED starts fresh by design.
	status := info.GetStatus()
	switch status {
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED,
		enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		// eligible
	default:
		return "", ErrNoReplayableHistory
	}

	runID := info.GetExecution().GetRunId()
	historyLen := info.GetHistoryLength()

	// Bounded guard: stop resetting a workflow that keeps re-failing at the same
	// point (deterministic error / replay wedge) — fall back to coarse restart.
	if !ps.resetGuard.Allow(workflowID, historyLen) {
		logging.Warn("[PauseService] Reset-attempt guard exhausted, falling back to coarse restart",
			"workflowID", workflowID,
			"chatID", chatID,
			"attempts", ps.resetGuard.Attempts(workflowID),
		)
		return "", ErrResetAttemptsExhausted
	}

	newRunID, err := ResetInterruptedWorkflow(ctx, ps.temporalClient, workflowID, runID, status)
	if err != nil {
		return "", fmt.Errorf("failed to reset interrupted workflow: %w", err)
	}
	ps.resetGuard.Record(workflowID, historyLen)

	logging.Info("[PauseService] Interrupted workflow reset for resume",
		"workflowID", workflowID,
		"chatID", chatID,
		"newRunID", newRunID,
		"priorStatus", status.String(),
	)
	return newRunID, nil
}
