// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	enums "go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// Service owns the run lifecycle: making a chat's run stop, and making it
// execute again. See the package doc for what that ownership covers.
type Service struct {
	repo     Repository
	temporal TemporalClient
	pause    PauseController
}

// NewService builds a run-lifecycle service over the chat store, Temporal, and
// the pause/reset controller.
func NewService(repo Repository, temporal TemporalClient, pause PauseController) *Service {
	return &Service{repo: repo, temporal: temporal, pause: pause}
}

// Pause stops the chat's run at its next step boundary, leaving it resumable.
//
// The run stays alive in Temporal, blocked in place — nothing about its
// position is written down, because the parked goroutine stack IS the position.
// A run that has already finished is not an error: the user's intent (stop it)
// is satisfied, and the controller reconciles the stale row instead.
func (s *Service) Pause(ctx context.Context, chatID string) error {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return err
	}
	return s.pause.PauseWorkflow(ctx, workflowID, chatID, "manual")
}

// Terminate forcefully ends a chat's run and makes the next message start a
// fresh run rather than resuming from position.
//
// This is a hard operator stop, not Temporal's cooperative cancellation. A run
// parked on a signal Await may never observe CancelWorkflow, and Terminate skips
// the workflow completion handler, so this method also performs the durable
// cleanup that handler would normally own: drop the checkpoint, move the root row
// with a CAS, void an open question, and drain workflow/thread descendants.
func (s *Service) Terminate(ctx context.Context, chatID string) error {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return err
	}

	if err := s.repo.DeleteWorkflowCheckpoint(ctx, workflowID); err != nil {
		logging.Warn("[runs] Failed to clear workflow checkpoint while terminating",
			"workflowID", workflowID, "error", err)
	}

	state, stateErr := s.State(ctx, workflowID)
	if stateErr == nil && (!state.Exists || !state.IsRunning) {
		s.reconcileStoppedRun(ctx, workflowID, db.Cancelled())
		return nil
	}
	if stateErr != nil {
		logging.Warn("[runs] Could not read Temporal state before terminating; terminating anyway",
			"workflowID", workflowID, "error", stateErr)
	}

	s.voidPendingQuestion(ctx, chatID)

	terminator, ok := s.temporal.(TemporalTerminator)
	if !ok {
		return fmt.Errorf("temporal client cannot terminate workflows")
	}
	if err := terminator.TerminateWorkflow(ctx, workflowID, "", "Workflow terminated by operator"); err != nil {
		logging.Warn("[runs] Failed to terminate workflow in Temporal; continuing to reconcile DB",
			"workflowID", workflowID, "error", err)
	}

	if err := s.markTerminated(ctx, workflowID); err != nil {
		return err
	}
	return nil
}

// Resume makes the chat's run execute again, and reports what a caller must
// render. It never reports HOW the run was restarted.
//
// The sequence is the whole policy this service exists to own:
//
//  1. Refuse a stuck run — the database says failed while Temporal says
//     running. No resume can reconcile that, so the user must branch.
//  2. Ask the controller to resume. It signals a live execution in place and
//     reset-replays a dead-but-replayable one; either way position comes from
//     Temporal, never from a stored record.
//  3. Re-read the run id from Temporal and write it back. A reset mints a NEW
//     run, so the id the chat holds is stale exactly when a reset happened —
//     and asking unconditionally is cheap and correct.
func (s *Service) Resume(ctx context.Context, chatID string) (ResumeOutcome, error) {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return ResumeOutcome{}, err
	}

	if s.isStuck(ctx, workflowID) {
		logging.Warn("[runs] Refusing to resume stuck run — database says failed while Temporal says running",
			"chatID", chatID, "workflowID", workflowID)
		return ResumeOutcome{Kind: OutcomeUnresumable, WorkflowID: workflowID}, nil
	}

	if err := s.pause.ResumeWorkflow(ctx, workflowID, chatID); err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			logging.Info("[runs] Run is gone from Temporal — caller must recover",
				"chatID", chatID, "workflowID", workflowID)
			return ResumeOutcome{Kind: OutcomeNeedsRecovery, WorkflowID: workflowID}, nil
		}
		return ResumeOutcome{}, fmt.Errorf("resume run %s: %w", workflowID, err)
	}

	return ResumeOutcome{
		Kind:       OutcomeResumed,
		WorkflowID: workflowID,
		RunID:      s.refreshRunID(ctx, chatID, workflowID),
	}, nil
}

// ResumeInterrupted restarts a run whose Temporal execution ended in a closed
// non-cancel state (failed, terminated, timed out) by resetting it to a
// replayable point and replaying.
//
// Replay is what makes this precise rather than coarse: because every
// sub-workflow runs inline in the root execution, replay rebuilds the entire
// nested engine stack, so a run that died deep inside a review loop resumes at
// the same iteration with its feedback intact. The coarse fallback can only
// re-enter a top-level node.
//
// OutcomeNeedsRestart is a normal result, not a failure — it means replay
// cannot serve this run and the caller should start a fresh one at the
// checkpoint.
func (s *Service) ResumeInterrupted(ctx context.Context, chatID string) (ResumeOutcome, error) {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return ResumeOutcome{}, err
	}

	newRunID, resumeErr := s.pause.ResumeInterruptedWorkflow(ctx, workflowID, chatID)
	if resumeErr == nil {
		s.RecordRun(ctx, chatID, workflowID, newRunID)
		logging.Info("[runs] Interrupted run reset-and-resumed (precise nested resume)",
			"chatID", chatID, "workflowID", workflowID, "newRunID", newRunID)
		return ResumeOutcome{Kind: OutcomeResumed, WorkflowID: workflowID, RunID: newRunID}, nil
	}

	// A run at the history cap is called out separately because the caller has
	// to tell the user: resetting forks from inside the oversized history and
	// dies within a few events, so without an explanation the chat just stops.
	if errors.Is(resumeErr, workflow.ErrHistoryLimitExceeded) {
		logging.Warn("[runs] Run hit Temporal's history limit — restart at checkpoint",
			"chatID", chatID, "workflowID", workflowID)
		return ResumeOutcome{
			Kind:                 OutcomeNeedsRestart,
			WorkflowID:           workflowID,
			HistoryLimitExceeded: true,
		}, nil
	}

	// Nothing to replay (ghost, past retention) or the bounded guard gave up
	// on a run that kept re-failing at the same point. Both are documented
	// fallbacks to the coarse restart.
	if errors.Is(resumeErr, workflow.ErrNoReplayableHistory) || errors.Is(resumeErr, workflow.ErrResetAttemptsExhausted) {
		logging.Info("[runs] Run is not reset-resumable — restart at checkpoint",
			"chatID", chatID, "workflowID", workflowID, "reason", resumeErr)
		return ResumeOutcome{Kind: OutcomeNeedsRestart, WorkflowID: workflowID}, nil
	}

	// An unexpected reset failure is still recoverable by the coarse restart,
	// so it is reported as such rather than failing the caller's request —
	// but it is logged at error, because unlike the cases above it is a bug
	// or an outage rather than a designed fallback.
	logging.Error("[runs] Reset-and-replay failed — restart at checkpoint",
		"chatID", chatID, "workflowID", workflowID, "error", resumeErr)
	return ResumeOutcome{Kind: OutcomeNeedsRestart, WorkflowID: workflowID}, nil
}

// ResumeViaSignal wakes a run with a domain signal instead of signal.resume,
// for a run parked on a channel that signal.resume does not release — an
// unanswered question being the case that matters.
//
// On success the run's status is marked running and the chat's run id is
// refreshed, because the delivery may have reset-replayed the execution into a
// new run. Both writes are best-effort: the signal is the step that actually
// restarts the run, and it has already succeeded.
func (s *Service) ResumeViaSignal(ctx context.Context, in ResumeViaSignalInput) (ResumeOutcome, error) {
	if err := s.pause.SignalWithRecovery(ctx, in.TargetWorkflowID, in.SignalName, in.SignalData); err != nil {
		logging.Info("[runs] Signal-parked run not reset-resumable — restart at checkpoint",
			"chatID", in.ChatID, "workflowID", in.WorkflowID,
			"signal", in.SignalName, "error", err)
		return ResumeOutcome{Kind: OutcomeNeedsRestart, WorkflowID: in.WorkflowID}, nil
	}

	if err := s.repo.UpdateWorkflowStatus(ctx, in.WorkflowID, db.Active()); err != nil {
		logging.Warn("[runs] Resumed run but failed to mark it running — the workflow or reconciler will correct it",
			"workflowID", in.WorkflowID, "error", err)
	}

	return ResumeOutcome{
		Kind:       OutcomeResumed,
		WorkflowID: in.WorkflowID,
		RunID:      s.refreshRunID(ctx, in.ChatID, in.WorkflowID),
	}, nil
}

// Inspect classifies a chat's run for a caller that must choose between
// recovery paths rather than simply resuming.
//
// It exists so the "is this stuck / is there anything to replay" judgement has
// one implementation. A caller that only wants to resume should call Resume,
// which performs the stuck check itself.
func (s *Service) Inspect(ctx context.Context, chatID string) (Inspection, error) {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return Inspection{}, err
	}

	state, stateErr := s.State(ctx, workflowID)
	if stateErr != nil || !state.Exists {
		// Temporal could not answer, or has no such execution. Neither is a
		// stuck run, and neither offers anything to replay.
		return Inspection{WorkflowID: workflowID}, nil
	}

	if state.IsRunning {
		// Open in Temporal. Stuck only if the row disagrees by claiming the
		// run already failed.
		wf, wfErr := s.repo.GetWorkflow(ctx, workflowID)
		stuck := wfErr == nil && wf != nil && wf.Status == db.Failed()
		return Inspection{WorkflowID: workflowID, Stuck: stuck}, nil
	}

	// Closed in Temporal, so there is recorded history a reset can replay.
	return Inspection{WorkflowID: workflowID, Recoverable: true}, nil
}

// State asks Temporal what an execution is actually doing.
//
// Temporal is the source of truth here and the workflow row is a cache of it,
// which is why every lifecycle decision in this package consults this rather
// than the row alone. Returns Exists=false — not an error — when Temporal has
// no such execution, since a lost or expired run is an expected state with its
// own recovery path.
func (s *Service) State(ctx context.Context, workflowID string) (RunState, error) {
	desc, err := s.temporal.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "NotFound") {
			return RunState{Exists: false}, nil
		}
		return RunState{}, fmt.Errorf("failed to query Temporal: %w", err)
	}

	if desc == nil || desc.WorkflowExecutionInfo == nil {
		return RunState{Exists: false}, nil
	}

	runID := ""
	if desc.WorkflowExecutionInfo.Execution != nil {
		runID = desc.WorkflowExecutionInfo.Execution.RunId
	}

	var status db.WorkflowStatus
	isRunning := false
	switch execStatus := desc.WorkflowExecutionInfo.Status; execStatus {
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING:
		// Open — which covers both actively executing and waiting for a
		// worker. The stuck check is what separates those.
		status = db.Active()
		isRunning = true
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		status = db.Completed()
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
		enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		// TERMINATED maps to Failed, not Cancelled: termination is a system or
		// operator kill (reconciler wedge recovery, manual terminate, conflict
		// policy), and maps to Failed here because Temporal cannot tell us which
		// component terminated it. The explicit operator terminate path overrides
		// this mapping in Terminate by dropping the checkpoint and writing
		// Cancelled through the database CAS before a later recovery decision can
		// treat it as resumable.
		status = db.Failed()
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
		status = db.Cancelled()
	case enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		// Continued — treat as running, since there is a new run carrying on.
		status = db.Active()
		isRunning = true
	default:
		// Unknown status — treat as completed to allow starting fresh.
		logging.Warn("[runs] Unknown Temporal workflow status",
			"workflowID", workflowID, "status", execStatus.String())
		status = db.Completed()
	}

	return RunState{Exists: true, Status: status, RunID: runID, IsRunning: isRunning}, nil
}

// RecordRun writes the workflow and run ids back onto the chat.
//
// Best-effort: a stale run id degrades later lookups but never stops a run that
// is already executing, and failing a request over it would be worse.
func (s *Service) RecordRun(ctx context.Context, chatID, workflowID, runID string) {
	chat, err := s.repo.GetChat(ctx, chatID)
	if err != nil {
		logging.Error("[runs] Failed to load chat to record run ids",
			"chatID", chatID, "error", err)
		return
	}

	chat.WorkflowID = &workflowID
	chat.RunID = &runID
	if err := s.repo.UpdateChat(ctx, chat); err != nil {
		logging.Error("[runs] Failed to record run ids on chat",
			"chatID", chatID, "error", err)
	}
}

// Reconcile brings a workflow row into line with the status Temporal reports,
// cascading a terminal repair through the run's descendants and threads.
//
// The cascade is the point. We are here precisely because the run ended without
// its own completion handler writing this status, which means the cascade that
// handler performs did not happen either — so every spawned workflow and thread
// row is still sitting at running or paused. Nothing else revisits a row with a
// parent, so skipping the cascade leaves the chat permanently "active" and the
// dead rows permanently listed.
//
// A run that stopped only because it is PAUSED is deliberately excluded: it has
// not ended, and draining its subtree would kill work that is coming back.
func (s *Service) Reconcile(ctx context.Context, workflowID string, dbStatus, temporalStatus db.WorkflowStatus) {
	if dbStatus == temporalStatus {
		return
	}

	logging.Debug("[runs] Reconciling run status: database differs from Temporal",
		"workflowID", workflowID, "dbStatus", dbStatus, "temporalStatus", temporalStatus)

	if err := s.repo.UpdateWorkflowStatus(ctx, workflowID, temporalStatus); err != nil {
		logging.Warn("[runs] Failed to reconcile run status",
			"workflowID", workflowID, "error", err)
		return
	}

	if !temporalStatus.IsStopped() || temporalStatus.StopReason == db.StopReasonPaused {
		return
	}

	// The subtree inherits the reason the run actually stopped, so a repaired
	// cancel does not read as a repaired success.
	reason := temporalStatus.StopReason
	if err := s.repo.CascadeTerminalStatusToDescendants(ctx, workflowID, reason); err != nil {
		logging.Warn("[runs] Failed to cascade reconciled terminal status to child workflows",
			"workflowID", workflowID, "error", err)
	}
	// Threads are not a workflows row and need their own cascade call — see
	// docs/incidents/2026-08-12-spawn-history-cap.md.
	if err := s.repo.CascadeTerminalStatusToThreadSubtree(ctx, workflowID, reason); err != nil {
		logging.Warn("[runs] Failed to cascade reconciled terminal status to threads",
			"workflowID", workflowID, "error", err)
	}
}

func (s *Service) reconcileStoppedRun(ctx context.Context, workflowID string, status db.WorkflowStatus) {
	wf, err := s.repo.GetWorkflow(ctx, workflowID)
	if err != nil {
		logging.Warn("[runs] Failed to load workflow before stopped-run reconciliation",
			"workflowID", workflowID, "error", err)
		return
	}
	if wf == nil || !wf.Status.Live() {
		return
	}
	s.Reconcile(ctx, workflowID, wf.Status, status)
}

func (s *Service) markTerminated(ctx context.Context, workflowID string) error {
	wf, err := s.repo.GetWorkflow(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("load workflow %s before terminate CAS: %w", workflowID, err)
	}
	if wf == nil || !wf.Status.Live() {
		return nil
	}

	swapped, err := s.repo.CompareAndSwapWorkflowStatus(ctx, workflowID, db.Cancelled(), wf.Status)
	if err != nil {
		return fmt.Errorf("mark terminated workflow %s: %w", workflowID, err)
	}
	if swapped {
		s.cascadeTerminated(ctx, workflowID)
	}
	return nil
}

func (s *Service) cascadeTerminated(ctx context.Context, workflowID string) {
	if err := s.repo.CascadeTerminalStatusToDescendants(ctx, workflowID, db.StopReasonCancelled); err != nil {
		logging.Warn("[runs] Failed to cascade terminated status to child workflows",
			"workflowID", workflowID, "error", err)
	}
	if err := s.repo.CascadeTerminalStatusToThreadSubtree(ctx, workflowID, db.StopReasonCancelled); err != nil {
		logging.Warn("[runs] Failed to cascade terminated status to threads",
			"workflowID", workflowID, "error", err)
	}
}

func (s *Service) voidPendingQuestion(ctx context.Context, chatID string) {
	q, err := s.repo.GetPendingQuestionByChatID(ctx, chatID)
	if err != nil {
		logging.Warn("[runs] Failed to look up pending question while terminating",
			"chatID", chatID, "error", err)
		return
	}
	if q == nil {
		return
	}

	terminatedResponse := `{"action":"terminated","reason":"workflow terminated by operator"}`
	if err := s.repo.ResolveQuestion(ctx, q.ID, &terminatedResponse); err != nil {
		logging.Warn("[runs] Failed to resolve pending question while terminating",
			"chatID", chatID, "questionID", q.ID, "error", err)
		return
	}

	if err := s.repo.EmitQuestionUpdate(ctx, q.ChatID, db.QuestionUpdate{
		QuestionID: q.ID,
		ChatID:     q.ChatID,
		WorkflowID: q.WorkflowID,
		ThreadID:   q.ThreadID,
		StepID:     q.StepID,
		Status:     "resolved",
	}); err != nil {
		logging.Warn("[runs] Failed to emit question resolved update while terminating",
			"chatID", chatID, "questionID", q.ID, "error", err)
	}
}

// chatRun resolves a chat id to the chat and its root workflow id, which every
// lifecycle entry point needs before it can do anything.
func (s *Service) chatRun(ctx context.Context, chatID string) (*db.Chat, string, error) {
	chat, err := s.repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, "", ErrChatNotFound
	}
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return nil, "", ErrNoWorkflow
	}
	return chat, workflowID, nil
}

// isStuck reports the one state no resume can serve: the database records the
// run as failed while Temporal still reports the execution running.
//
// Only a row that positively says Failed counts. A Temporal query that fails is
// deliberately not treated as stuck — refusing to resume because we could not
// reach Temporal would strand a perfectly resumable chat.
func (s *Service) isStuck(ctx context.Context, workflowID string) bool {
	wf, err := s.repo.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil || wf.Status != db.Failed() {
		return false
	}
	state, err := s.State(ctx, workflowID)
	return err == nil && state.Exists && state.IsRunning
}

// refreshRunID re-reads the authoritative run id from Temporal and writes it
// back to the chat when it has changed.
//
// A resume may have RESET the execution, which mints a new run, so the id the
// chat holds is stale exactly when a reset happened. Asking Temporal
// unconditionally is cheap and correct; falling back to the stored id keeps a
// successful resume from being reported as a failure just because the follow-up
// read did not land.
func (s *Service) refreshRunID(ctx context.Context, chatID, workflowID string) string {
	state, err := s.State(ctx, workflowID)
	if err == nil && state.Exists {
		s.recordRunIfChanged(ctx, chatID, workflowID, state.RunID)
		return state.RunID
	}

	if chat, chatErr := s.repo.GetChat(ctx, chatID); chatErr == nil && chat.RunID != nil {
		return *chat.RunID
	}
	return ""
}

// recordRunIfChanged writes the run id back only when it differs from what the
// chat already holds, so an unchanged resume costs no write.
func (s *Service) recordRunIfChanged(ctx context.Context, chatID, workflowID, runID string) {
	chat, err := s.repo.GetChat(ctx, chatID)
	if err != nil {
		logging.Error("[runs] Failed to load chat to record run ids",
			"chatID", chatID, "error", err)
		return
	}
	if chat.RunID != nil && *chat.RunID == runID {
		return
	}

	chat.WorkflowID = &workflowID
	chat.RunID = &runID
	if err := s.repo.UpdateChat(ctx, chat); err != nil {
		logging.Error("[runs] Failed to record run ids on chat",
			"chatID", chatID, "error", err)
	}
}
