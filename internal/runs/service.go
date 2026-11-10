// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	enums "go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// Service owns the run lifecycle: making a chat's run stop, and making it
// execute again. See the package doc for what that ownership covers.
type Service struct {
	repo     Repository
	temporal TemporalClient
	pause    PauseController

	// cancelGrace, cancelSettle and cancelPoll override cancelGrace,
	// cancelSettleTimeout and cancelPollInterval when positive; tests shrink
	// them.
	cancelGrace, cancelSettle, cancelPoll time.Duration
	// now and sleep are the clock the cancel waits read; nil is the real
	// one. Tests replace them so no assertion depends on wall time.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration)
}

// NewService builds a run-lifecycle service over the chat store, Temporal, and
// the pause/reset controller.
func NewService(repo Repository, temporal TemporalClient, pause PauseController) *Service {
	return &Service{repo: repo, temporal: temporal, pause: pause}
}

// LockRunControl serializes the chat's run-control operations — pause, resume,
// and SendMessage's choice between resuming a run and waking a running one —
// and returns the func that ends the critical section.
//
// Each of those pairs a Temporal signal with a workflow-status write, in that
// order, and the two cannot commit together. Unserialized, a send that reads
// the status between a pause's signal and its write sees "running", routes the
// message as a thread wake, and the wake never releases the pause gate. On
// chat 264b5697 the user pressed ESC and sent ~420ms later; the send read the
// row ~10ms before the pause wrote it, and the run sat paused with the message
// unread until the user sent again. Holding this from the routing read through
// delivery makes every pair of these operations take effect in the order the
// server received them.
//
// It never fails. If the lock cannot be had (lock timeout, database trouble)
// the caller proceeds unserialized — the behavior before the lock existed —
// because refusing a pause or a send is worse than racing one. That is logged
// loudly, since it is exactly the condition that strands messages.
func (s *Service) LockRunControl(ctx context.Context, chatID string) (release func()) {
	start := time.Now()
	release, err := s.repo.LockChatRunControl(ctx, chatID)
	if err != nil {
		logging.WarnContext(ctx, "[runs] Could not take the run-control lock; proceeding unserialized — a concurrent pause and send may be applied out of order",
			"chatID", chatID, "waited", time.Since(start), "error", err)
		return func() {}
	}
	if waited := time.Since(start); waited > runControlWaitWorthLogging {
		logging.InfoContext(ctx, "[runs] Waited for another run-control operation on this chat",
			"chatID", chatID, "waited", waited)
	}
	return release
}

// runControlWaitWorthLogging is long enough that an uncontended acquire (one
// round trip) never logs, and short enough that waiting out a concurrent pause
// always does — that line is the evidence a future race investigation needs.
const runControlWaitWorthLogging = 50 * time.Millisecond

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

// Terminate ends a chat's run for good and makes the next message start a
// fresh run rather than resuming from position.
//
// It stops the run cooperatively first: Temporal is asked to CANCEL it, which
// runs the run's own completion code. Its in-flight activities are cancelled,
// which kills the commands they were running on the machine; its terminal
// handlers run, so a workflow releases what it holds (a lease, a claim, a
// label); and it writes its own cancelled status and cascade. Terminate waits
// for the run to acknowledge the cancel and then for its terminal state to be
// durable, reconciling the row itself when the execution closed without
// writing it (cancelCooperatively has the phases and their bounds).
//
// A run that does not report ending in time — parked on a signal Await that
// never observes the cancel, or with no worker to run it — is hard-terminated,
// as every cancel used to be. A terminate runs no workflow code, so then this
// method performs the durable cleanup the completion handler would have owned:
// move the root row with a CAS and drain workflow/thread descendants. Either
// way the checkpoint is dropped and open questions are voided first.
func (s *Service) Terminate(ctx context.Context, chatID string) error {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return err
	}

	if err := s.repo.DeleteWorkflowCheckpoint(ctx, workflowID); err != nil {
		logging.WarnContext(ctx, "[runs] Failed to clear workflow checkpoint while terminating",
			"workflowID", workflowID, "error", err)
	}

	state, stateErr := s.State(ctx, workflowID)
	if stateErr == nil && (!state.Exists || !state.IsRunning) {
		s.reconcileStoppedRun(ctx, workflowID, db.Cancelled())
		s.clearUsageLimitWait(ctx, chatID)
		return nil
	}
	if stateErr != nil {
		logging.WarnContext(ctx, "[runs] Could not read Temporal state before terminating; terminating anyway",
			"workflowID", workflowID, "error", stateErr)
	}

	s.voidPendingQuestions(ctx, chatID)

	// Only a run Temporal could be read as live is asked: one whose state
	// could not be read is terminated outright, as before.
	if stateErr == nil && s.cancelCooperatively(ctx, workflowID) {
		s.clearUsageLimitWait(ctx, chatID)
		return nil
	}

	terminator, ok := s.temporal.(TemporalTerminator)
	if !ok {
		return fmt.Errorf("temporal client cannot terminate workflows")
	}
	if err := terminator.TerminateWorkflow(ctx, workflowID, "", "Workflow terminated by operator"); err != nil {
		logging.WarnContext(ctx, "[runs] Failed to terminate workflow in Temporal; continuing to reconcile DB",
			"workflowID", workflowID, "error", err)
	}

	if err := s.markTerminated(ctx, workflowID); err != nil {
		return err
	}
	s.clearUsageLimitWait(ctx, chatID)
	return nil
}

// cancelGrace bounds how long Terminate waits for a cancelled run to
// ACKNOWLEDGE the cancel — report that it has left its graph — before it
// hard-terminates the run. A run that observes the cancel reaches its
// completion code within one workflow task — a second or two on a loaded
// worker — so this is long enough for a slow worker and short enough that a
// caller is not held waiting on a run that will never answer.
const cancelGrace = 15 * time.Second

// cancelSettleTimeout bounds how long Terminate then waits for the cancelled
// run to SETTLE: its terminal status and cascade durably written, or its
// execution closed. A run with no finally handler settles within seconds of
// acknowledging; one whose finally runs longer is still executing that
// handler when this elapses, and is left to finish it (see
// cancelCooperatively).
const cancelSettleTimeout = 30 * time.Second

// cancelPollInterval is how often Terminate asks a cancelled run how far it
// has got.
const cancelPollInterval = 250 * time.Millisecond

// cancelCooperatively asks Temporal to cancel workflowID and reports whether
// the run is stopping itself (true) or the caller must hard-terminate it
// (false).
//
// Two phases, because a run's word that it is ending is not the same as its
// bookkeeping being done:
//
//  1. Acknowledge, within cancelGrace: the run reports, through
//     model.RunStopQuery, that it has entered its completion code, or
//     Temporal reports it closed. Neither yet says the database agrees: the
//     query is answered the moment the completion defer starts, before its
//     handlers and its status write, and an execution that closed may have
//     closed some other way (it completed concurrently with this cancel, or
//     its own status write failed). No acknowledgement means the run will
//     not stop itself: terminate it.
//  2. Settle, within cancelSettleTimeout: the execution has CLOSED. Closure
//     is the only fact that ends the run's own writes, so it is the only
//     thing settlement rests on — never the row: a terminal row can be
//     written while the run still executes (a completion that raced the
//     cancel commits `completed` and then runs on), so a row alone proves
//     neither that the run stopped nor that its bookkeeping is whole. Once
//     closed, settleClosedRun makes the row agree (see there).
//
// A run still open when phase 2 elapses is still executing its completion
// code — its finally handler, or a racing completion's bookkeeping. Its own
// completion code writes its terminal status when that ends, inside the
// handler's own budget, and terminating it would cut off the cleanup the
// cancel was cooperative for. So it is left running, deliberately, and said
// so; Terminate returns, because the run will stop by itself.
func (s *Service) cancelCooperatively(ctx context.Context, workflowID string) bool {
	canceller, ok := s.temporal.(TemporalCanceller)
	if !ok {
		return false
	}
	if err := canceller.CancelWorkflow(ctx, workflowID, ""); err != nil {
		logging.WarnContext(ctx, "[runs] Could not ask Temporal to cancel the run; terminating it instead",
			"workflowID", workflowID, "error", err)
		return false
	}
	grace, settle, poll := s.cancelGrace, s.cancelSettle, s.cancelPoll
	if grace <= 0 {
		grace = cancelGrace
	}
	if settle <= 0 {
		settle = cancelSettleTimeout
	}
	if poll <= 0 {
		poll = cancelPollInterval
	}

	if !s.pollUntil(ctx, grace, poll, func() bool { return s.runAcknowledgedCancel(ctx, canceller, workflowID) }) {
		logging.WarnContext(ctx, "[runs] Cancelled run did not acknowledge the cancel in time; terminating it",
			"workflowID", workflowID, "grace", grace)
		return false
	}
	if !s.pollUntil(ctx, settle, poll, func() bool { return s.cancelledRunSettled(ctx, workflowID) }) {
		logging.InfoContext(ctx, "[runs] Cancelled run is still executing its completion code; it writes its own terminal status when that ends",
			"workflowID", workflowID, "waited", settle)
	}
	return true
}

// pollUntil calls done every poll until it reports true, for at most budget
// (and one last time at the end of it). It reports whether done did. It reads
// the service's clock, which tests replace.
func (s *Service) pollUntil(ctx context.Context, budget, poll time.Duration, done func() bool) bool {
	now, sleep := s.now, s.sleep
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	deadline := now().Add(budget)
	for {
		if done() {
			return true
		}
		if now().After(deadline) || ctx.Err() != nil {
			return false
		}
		sleep(ctx, poll)
	}
}

// sleepCtx waits d, or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// runAcknowledgedCancel reports whether a cancelled run has left its graph:
// it says so itself, or Temporal reports it closed. Neither means its
// bookkeeping is done (cancelledRunSettled).
func (s *Service) runAcknowledgedCancel(ctx context.Context, canceller TemporalCanceller, workflowID string) bool {
	if value, err := canceller.QueryWorkflow(ctx, workflowID, "", model.RunStopQuery); err == nil {
		var state model.RunStopState
		if value != nil && value.Get(&state) == nil && state.Ending {
			return true
		}
	}
	state, err := s.State(ctx, workflowID)
	return err == nil && (!state.Exists || !state.IsRunning)
}

// cancelledRunSettled reports whether a cancelled run has settled: its
// execution has closed, so nothing of the run's own will write again, and
// settleClosedRun has made its row agree. An execution Temporal reports open,
// or whose state cannot be read, has not settled, whatever its row says.
func (s *Service) cancelledRunSettled(ctx context.Context, workflowID string) bool {
	state, err := s.State(ctx, workflowID)
	if err != nil || (state.Exists && state.IsRunning) {
		return false
	}
	s.settleClosedRun(ctx, workflowID)
	return true
}

// settleClosedRun makes the row of a cancelled run whose execution has closed
// agree with how it ended. Which ending won is the run's to say, not the
// cancel's:
//
//   - A row the closed execution left LIVE never got its terminal write (the
//     write failed, or the run closed some other way): nothing else will
//     write it, so it is moved to cancelled with its cascade, as Terminate
//     does for a run it finds closed.
//   - A TERMINAL row is the run's own verdict and stands: a completion that
//     raced the cancel and committed `completed` (or `failed`) first is how
//     that run ended, and the cancel does not overwrite it. Its cascade is
//     re-asserted at the row's own stop reason, so a terminal row whose
//     descendants were not all ended — however it was written — leaves none
//     running. The cascade ends only live descendants, so for the usual row,
//     whose writer committed status and cascade together, it is a no-op.
func (s *Service) settleClosedRun(ctx context.Context, workflowID string) {
	wf, err := s.repo.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		if err != nil {
			logging.WarnContext(ctx, "[runs] Could not read a cancelled run's row to settle it",
				"workflowID", workflowID, "error", err)
		}
		return
	}
	if wf.Status.Live() {
		s.reconcileStoppedRun(ctx, workflowID, db.Cancelled())
		return
	}
	reason := wf.Status.StopReason
	if err := s.repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		return s.cascadeTerminal(txCtx, workflowID, reason)
	}); err != nil {
		logging.WarnContext(ctx, "[runs] Could not re-assert a cancelled run's terminal cascade",
			"workflowID", workflowID, "status", wf.Status.Label(), "error", err)
	}
}

// clearUsageLimitWait releases the chat's usage-limit wait markers. A
// terminated run executes no code, so the workflow cannot clear its own; left
// behind, the marker would read as a wait nobody is in.
func (s *Service) clearUsageLimitWait(ctx context.Context, chatID string) {
	if err := s.repo.ClearUsageLimitWait(ctx, chatID, "", time.Now()); err != nil {
		logging.WarnContext(ctx, "[runs] Failed to clear the usage-limit wait marker of a stopped run",
			"chatID", chatID, "error", err)
	}
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
//
// inputUpdate, when non-empty, reaches the run before its resume does — the
// live run or the one a reset made (PauseController.ResumeWorkflow).
func (s *Service) Resume(ctx context.Context, chatID string, inputUpdate map[string]interface{}) (ResumeOutcome, error) {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return ResumeOutcome{}, err
	}

	if s.isStuck(ctx, workflowID) {
		logging.WarnContext(ctx, "[runs] Refusing to resume stuck run — database says failed while Temporal says running",
			"chatID", chatID, "workflowID", workflowID)
		return ResumeOutcome{Kind: OutcomeUnresumable, WorkflowID: workflowID}, nil
	}

	if err := s.pause.ResumeWorkflow(ctx, workflowID, chatID, inputUpdate); err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			logging.InfoContext(ctx, "[runs] Run is gone from Temporal — caller must recover",
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
//
// inputUpdate, when non-empty, reaches the replayed run before its resume:
// replay rebuilds the run from the inputs its history recorded, so a change
// the caller is carrying would otherwise never reach it.
func (s *Service) ResumeInterrupted(ctx context.Context, chatID string, inputUpdate map[string]interface{}) (ResumeOutcome, error) {
	_, workflowID, err := s.chatRun(ctx, chatID)
	if err != nil {
		return ResumeOutcome{}, err
	}

	newRunID, resumeErr := s.pause.ResumeInterruptedWorkflow(ctx, workflowID, chatID, inputUpdate)
	if resumeErr == nil {
		s.RecordRun(ctx, chatID, workflowID, newRunID)
		logging.InfoContext(ctx, "[runs] Interrupted run reset-and-resumed (precise nested resume)",
			"chatID", chatID, "workflowID", workflowID, "newRunID", newRunID)
		return ResumeOutcome{Kind: OutcomeResumed, WorkflowID: workflowID, RunID: newRunID}, nil
	}

	// A run at the history cap is called out separately because the caller has
	// to tell the user: resetting forks from inside the oversized history and
	// dies within a few events, so without an explanation the chat just stops.
	if errors.Is(resumeErr, workflow.ErrHistoryLimitExceeded) {
		logging.WarnContext(ctx, "[runs] Run hit Temporal's history limit — restart at checkpoint",
			"chatID", chatID, "workflowID", workflowID)
		return ResumeOutcome{
			Kind:                 OutcomeNeedsRestart,
			WorkflowID:           workflowID,
			HistoryLimitExceeded: true,
		}, nil
	}

	// Nothing to replay (ghost, past retention), the bounded guard gave up on
	// a run that kept re-failing at the same point, or a history the current
	// code cannot replay (a reset would wedge the new run). All are documented
	// fallbacks to the coarse restart.
	if errors.Is(resumeErr, workflow.ErrNoReplayableHistory) || errors.Is(resumeErr, workflow.ErrResetAttemptsExhausted) ||
		errors.Is(resumeErr, workflow.ErrReplayDiverged) {
		logging.InfoContext(ctx, "[runs] Run is not reset-resumable — restart at checkpoint",
			"chatID", chatID, "workflowID", workflowID, "reason", resumeErr)
		return ResumeOutcome{Kind: OutcomeNeedsRestart, WorkflowID: workflowID}, nil
	}

	// An unexpected reset failure is still recoverable by the coarse restart,
	// so it is reported as such rather than failing the caller's request —
	// but it is logged at error, because unlike the cases above it is a bug
	// or an outage rather than a designed fallback.
	logging.ErrorContext(ctx, "[runs] Reset-and-replay failed — restart at checkpoint",
		"chatID", chatID, "workflowID", workflowID, "error", resumeErr)
	return ResumeOutcome{Kind: OutcomeNeedsRestart, WorkflowID: workflowID}, nil
}

// ResumeViaSignal wakes a run with a domain signal instead of signal.resume,
// for a run parked on a channel that signal.resume does not release — an
// unanswered question being the case that matters.
//
// On success the run's status is marked running — unless the woken run has
// already written one of its own — and the chat's run id is refreshed,
// because the delivery may have reset-replayed the execution into a new run. Both writes are best-effort: the signal is the step that actually
// restarts the run, and it has already succeeded.
func (s *Service) ResumeViaSignal(ctx context.Context, in ResumeViaSignalInput) (ResumeOutcome, error) {
	before, known := workflow.StatusBeforeWake(ctx, s.repo, in.WorkflowID)
	if err := s.pause.SignalWithRecovery(ctx, in.TargetWorkflowID, in.SignalName, in.SignalData, in.InputUpdate); err != nil {
		logging.InfoContext(ctx, "[runs] Signal-parked run not reset-resumable — restart at checkpoint",
			"chatID", in.ChatID, "workflowID", in.WorkflowID,
			"signal", in.SignalName, "error", err)
		return ResumeOutcome{Kind: OutcomeNeedsRestart, WorkflowID: in.WorkflowID}, nil
	}

	// The woken run may already have moved on (workflow.MarkWokenRunActive).
	workflow.MarkWokenRunActive(ctx, s.repo, in.WorkflowID, before, known)

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
		logging.WarnContext(ctx, "[runs] Unknown Temporal workflow status",
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
		logging.ErrorContext(ctx, "[runs] Failed to load chat to record run ids",
			"chatID", chatID, "error", err)
		return
	}

	chat.WorkflowID = &workflowID
	chat.RunID = &runID
	if err := s.repo.UpdateChat(ctx, chat); err != nil {
		logging.ErrorContext(ctx, "[runs] Failed to record run ids on chat",
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

	logging.DebugContext(ctx, "[runs] Reconciling run status: database differs from Temporal",
		"workflowID", workflowID, "dbStatus", dbStatus, "temporalStatus", temporalStatus)

	// The status and the cascade commit together: written apart, the
	// reconciler's thread reap could run between them and close the threads
	// itself, reporting a cascade that was one statement away as a missed one.
	err := s.repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := s.repo.UpdateWorkflowStatus(txCtx, workflowID, temporalStatus); err != nil {
			return err
		}
		if !temporalStatus.IsStopped() || temporalStatus.StopReason == db.StopReasonPaused {
			return nil
		}
		// The subtree inherits the reason the run actually stopped, so a
		// repaired cancel does not read as a repaired success.
		return s.cascadeTerminal(txCtx, workflowID, temporalStatus.StopReason)
	})
	if err != nil {
		logging.WarnContext(ctx, "[runs] Failed to reconcile run status",
			"workflowID", workflowID, "error", err)
	}
}

// cascadeTerminal ends workflowID's descendant workflows and their threads at
// reason. Threads are not a workflows row and need their own cascade call —
// see dev-docs/incidents/2026-08-12-spawn-history-cap.md.
func (s *Service) cascadeTerminal(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error {
	if err := s.repo.CascadeTerminalStatusToDescendants(ctx, workflowID, reason); err != nil {
		return fmt.Errorf("cascade terminal status to child workflows of %s: %w", workflowID, err)
	}
	if err := s.repo.CascadeTerminalStatusToThreadSubtree(ctx, workflowID, reason); err != nil {
		return fmt.Errorf("cascade terminal status to threads of %s: %w", workflowID, err)
	}
	return nil
}

func (s *Service) reconcileStoppedRun(ctx context.Context, workflowID string, status db.WorkflowStatus) {
	wf, err := s.repo.GetWorkflow(ctx, workflowID)
	if err != nil {
		logging.WarnContext(ctx, "[runs] Failed to load workflow before stopped-run reconciliation",
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

	// The terminate ran no workflow code, so this is the only cascade the run
	// gets — in the same commit as its status, like every terminal write.
	return s.repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		swapped, err := s.repo.CompareAndSwapWorkflowStatus(txCtx, workflowID, db.Cancelled(), wf.Status)
		if err != nil {
			return fmt.Errorf("mark terminated workflow %s: %w", workflowID, err)
		}
		if !swapped {
			return nil
		}
		return s.cascadeTerminal(txCtx, workflowID, db.StopReasonCancelled)
	})
}

func (s *Service) voidPendingQuestions(ctx context.Context, chatID string) {
	// Every pending question, not the newest: each spawned agent is a thread of
	// the run being terminated and can be parked on its own question.
	questions, err := s.repo.ListPendingQuestionsByChat(ctx, chatID)
	if err != nil {
		logging.WarnContext(ctx, "[runs] Failed to look up pending questions while terminating",
			"chatID", chatID, "error", err)
		return
	}

	terminatedResponse := `{"action":"terminated","reason":"workflow terminated by operator"}`
	for _, q := range questions {
		if err := s.repo.ResolveQuestion(ctx, q.ID, &terminatedResponse); err != nil {
			logging.WarnContext(ctx, "[runs] Failed to resolve pending question while terminating",
				"chatID", chatID, "questionID", q.ID, "error", err)
			continue
		}

		if err := s.repo.EmitQuestionUpdate(ctx, q.ChatID, db.QuestionUpdate{
			QuestionID: q.ID,
			ChatID:     q.ChatID,
			WorkflowID: q.WorkflowID,
			ThreadID:   q.ThreadID,
			StepID:     q.StepID,
			Status:     "resolved",
		}); err != nil {
			logging.WarnContext(ctx, "[runs] Failed to emit question resolved update while terminating",
				"chatID", chatID, "questionID", q.ID, "error", err)
		}
	}
}

// chatRun resolves a chat id to the chat and its root workflow id, which every
// lifecycle entry point needs before it can do anything.
func (s *Service) chatRun(ctx context.Context, chatID string) (*db.Chat, string, error) {
	chat, err := s.repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, "", fmt.Errorf("load chat %s: %w", chatID, err)
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
		logging.ErrorContext(ctx, "[runs] Failed to load chat to record run ids",
			"chatID", chatID, "error", err)
		return
	}
	if chat.RunID != nil && *chat.RunID == runID {
		return
	}

	chat.WorkflowID = &workflowID
	chat.RunID = &runID
	if err := s.repo.UpdateChat(ctx, chat); err != nil {
		logging.ErrorContext(ctx, "[runs] Failed to record run ids on chat",
			"chatID", chatID, "error", err)
	}
}
