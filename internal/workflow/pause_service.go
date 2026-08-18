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
	"github.com/reliant-labs/reliant/internal/observability"
)

// Resume outcome classes for metrics/alerting. These are the label values of
// reliant_workflow_resume_outcome_total; keep them in sync with the metric's
// help text in internal/observability/metrics.go.
//
// The split that matters is reset_replay versus everything else. Reset-and-
// replay is the resume mechanism and is strictly better than any checkpoint we
// could write — it rebuilds the nested engine stack AND the in-memory node
// outputs. The other four labels are the cases it provably cannot serve, and
// counting them is what decides whether a position stack ever needs a read
// side. See specs/workflow-lifecycle-refactor.md, "Adversarial review".
const (
	resumeOutcomeResetReplay            = "reset_replay"
	resumeOutcomeHistoryLimitExceeded   = "history_limit_exceeded"
	resumeOutcomeNoReplayableHistory    = "no_replayable_history"
	resumeOutcomeResetAttemptsExhausted = "reset_attempts_exhausted"
	resumeOutcomeResetError             = "reset_error"
)

// recordResumeOutcome counts one resume attempt and logs it with the same
// label. Both, always: the counter is what makes the ratio queryable where
// Prometheus is scraped, and the log is what makes it recoverable where it is
// not (a single-user desktop install exports no metrics, and those installs
// are exactly where the long-running chats that hit the history cap live).
func recordResumeOutcome(outcome, workflowID, chatID string, detail error) {
	observability.WorkflowResumeOutcomeTotal.WithLabelValues(outcome).Inc()

	// One stable message with a varying label, so the ratio is greppable:
	//   rg '\[ResumeOutcome\]' | grep -o 'outcome=[a-z_]*' | sort | uniq -c
	args := []interface{}{
		"outcome", outcome,
		"workflowID", workflowID,
		"chatID", chatID,
		"replayServed", outcome == resumeOutcomeResetReplay,
	}
	if detail != nil {
		args = append(args, "reason", detail.Error())
	}
	logging.Info("[ResumeOutcome] Interrupted-workflow resume attempt settled", args...)
}

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

// ErrHistoryLimitExceeded is returned when the interrupted run died because it
// exhausted Temporal's per-execution history limit. Reset-and-replay CANNOT
// recover this class: a reset forks a new run from a point INSIDE the oversized
// history, so the new run is born with essentially the same event count and has
// only a handful of events of headroom before Temporal terminates it again.
//
// Observed on a real chat: a run terminated at 51,201 events; the reset forked
// at event 51,194, the new run started at 51,198, executed two steps, and was
// terminated at 51,199. Each attempt burns a couple of events and dies, so the
// user sees "send a message" do nothing except produce one reply.
//
// The caller falls back to the coarse fresh-restart-at-checkpoint, which starts
// a genuinely new execution with EMPTY history and re-enters at the saved
// position, using thread history as conversation truth.
//
// ── Relationship to ContinueAsNew ─────────────────────────────────────────────
//
// This sentinel is damage control: it converts a wedged chat into a recoverable
// one AFTER the wall has been hit. Preventing runs from reaching the wall is
// the job of ContinueAsNew, now implemented in the runtime package (see
// runtime/continue_as_new.go). At the top-level agent loop's iteration
// boundary — the same deterministic point where the position checkpoint is
// written — a run past continueAsNewEventThreshold events voluntarily ends and
// starts a fresh execution under the same workflow ID with empty history,
// carrying {node_id, loop_iteration} forward as WorkflowInput.Resume.
//
// This path therefore remains reachable but should be rare: it now covers runs
// that predate the ContinueAsNew version gate, and runs whose boundary never
// went quiescent long enough to hand off (a background spawn in flight for
// tens of thousands of events).
var ErrHistoryLimitExceeded = errors.New("temporal history limit exceeded")

// temporalHistoryLimitHeadroom is how close to Temporal's per-execution history
// cap a run must be before reset-and-replay is considered futile.
//
// The cap is 51,200 events. The headroom is deliberately generous relative to
// the ~2-4 events a reset actually gets: a run this close cannot make
// meaningful progress before being terminated again, and routing it to the
// coarse restart one attempt early costs nothing, while routing it to a reset
// one attempt too late costs the user another dead run and another confusing
// "I sent a message and nothing happened".
const (
	temporalHistoryCountLimit = 51200
	temporalHistoryHeadroom   = 500
)

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
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.Paused()); err != nil {
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
	reason := db.StopReasonCompleted // default if we can't determine

	descResp, err := ps.temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err == nil && descResp != nil && descResp.WorkflowExecutionInfo != nil {
		switch descResp.WorkflowExecutionInfo.Status {
		case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
			reason = db.StopReasonCompleted
		case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
			enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
			// TERMINATED = system/operator kill → Failed (resumable at
			// position). Only CANCELED (user cancel) maps to Cancelled.
			// TIMED_OUT lands here too, which is why there is no separate
			// "expired" reason: it has always been recorded as a failure.
			reason = db.StopReasonFailed
		case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
			reason = db.StopReasonCancelled
		default:
			reason = db.StopReasonCompleted
		}
	}

	status := db.Stopped(reason)
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, status); err != nil {
		logging.Error("[PauseService] Failed to reconcile workflow status",
			"workflowID", workflowID,
			"targetStatus", status.Label(),
			"error", err,
		)
		return
	}

	// Cascade to child workflows, at the status the run actually reached. When a
	// root workflow's Temporal execution has expired, children's status
	// notifications were likely lost too — leaving them stuck at active/paused
	// and the chat permanently "active".
	if err := ps.database.CascadeTerminalStatusToDescendants(ctx, workflowID, reason); err != nil {
		logging.Error("[PauseService] Failed to cascade terminal status to child workflows",
			"workflowID", workflowID,
			"error", err,
		)
	}
	// Threads are not a workflows row and need their own cascade call — see
	// docs/incidents/2026-08-12-spawn-history-cap.md.
	if err := ps.database.CascadeTerminalStatusToThreadSubtree(ctx, workflowID, reason); err != nil {
		logging.Error("[PauseService] Failed to cascade terminal status to threads",
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
		case errors.Is(resetErr, ErrResetAttemptsExhausted), errors.Is(resetErr, ErrHistoryLimitExceeded):
			// Bounded, or at the history cap where resetting is futile —
			// surface so callers fall back to the coarse fresh restart.
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
			// A row that is parked awaiting resume is the one state where a
			// failed reset is not "the workflow is gone" — it is a workflow
			// that legitimately has nothing running to signal. This used to
			// also admit an EXPIRED status; nothing ever wrote that value
			// (Temporal TIMED_OUT is recorded as a failure), so the case was
			// unreachable and is not carried forward.
			if wf.Status.StopReason != db.StopReasonPaused {
				return fmt.Errorf("%w: %s (status: %s)", ErrWorkflowNotFound, workflowID, wf.Status.Label())
			}
		}
		return fmt.Errorf("failed to resume workflow: %w", err)
	}

	// Signal sent successfully — update DB status to running
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
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
	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
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
	// The reset re-executes any self-pause during replay, and the replayed
	// pause can re-arm AFTER this signal is applied — the resume coordinator
	// HOLDS a resume that arrives while no pause is armed (see
	// pauseCoordinator's resume-hold) so it still releases that pause instead
	// of being consumed as a no-op epoch bump and leaving the run parked
	// forever. Harmless for a run that never pauses: the held resume is
	// discarded by the next explicit signal.pause.
	if err := ps.temporalClient.SignalWorkflow(ctx, workflowID, newRunID, SignalResume, nil); err != nil {
		return "", fmt.Errorf("failed to send resume signal after reset: %w", err)
	}

	if err := ps.database.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
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
			recordResumeOutcome(resumeOutcomeNoReplayableHistory, workflowID, chatID, err)
			return "", ErrNoReplayableHistory
		}
		recordResumeOutcome(resumeOutcomeResetError, workflowID, chatID, err)
		return "", fmt.Errorf("failed to describe interrupted workflow: %w", err)
	}
	info := desc.GetWorkflowExecutionInfo()
	if info == nil || info.GetExecution() == nil {
		recordResumeOutcome(resumeOutcomeNoReplayableHistory, workflowID, chatID, nil)
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
		recordResumeOutcome(resumeOutcomeNoReplayableHistory, workflowID, chatID, nil)
		return "", ErrNoReplayableHistory
	}

	runID := info.GetExecution().GetRunId()
	historyLen := info.GetHistoryLength()

	// A run at the history cap cannot be rescued by resetting: the reset point
	// lives inside the oversized history, so the new run inherits it and dies
	// within a few events. Send it to the coarse fresh-restart instead, which
	// starts an execution with empty history. See ErrHistoryLimitExceeded.
	if historyLen >= temporalHistoryCountLimit-temporalHistoryHeadroom {
		logging.Warn("[PauseService] Workflow is at Temporal's history limit; reset cannot recover it, falling back to fresh restart",
			"workflowID", workflowID,
			"chatID", chatID,
			"historyLength", historyLen,
			"limit", temporalHistoryCountLimit,
		)
		recordResumeOutcome(resumeOutcomeHistoryLimitExceeded, workflowID, chatID, nil)
		return "", ErrHistoryLimitExceeded
	}

	// Bounded guard: stop resetting a workflow that keeps re-failing at the same
	// point (deterministic error / replay wedge) — fall back to coarse restart.
	if !ps.resetGuard.Allow(workflowID, historyLen) {
		logging.Warn("[PauseService] Reset-attempt guard exhausted, falling back to coarse restart",
			"workflowID", workflowID,
			"chatID", chatID,
			"attempts", ps.resetGuard.Attempts(workflowID),
		)
		recordResumeOutcome(resumeOutcomeResetAttemptsExhausted, workflowID, chatID, nil)
		return "", ErrResetAttemptsExhausted
	}

	newRunID, err := ResetInterruptedWorkflow(ctx, ps.temporalClient, workflowID, runID, status)
	if err != nil {
		recordResumeOutcome(resumeOutcomeResetError, workflowID, chatID, err)
		return "", fmt.Errorf("failed to reset interrupted workflow: %w", err)
	}
	ps.resetGuard.Record(workflowID, historyLen)

	// The good path: replay rebuilt the nested stack, including in-memory node
	// outputs that no checkpoint could restore.
	recordResumeOutcome(resumeOutcomeResetReplay, workflowID, chatID, nil)

	logging.Info("[PauseService] Interrupted workflow reset for resume",
		"workflowID", workflowID,
		"chatID", chatID,
		"newRunID", newRunID,
		"priorStatus", status.String(),
	)
	return newRunID, nil
}
