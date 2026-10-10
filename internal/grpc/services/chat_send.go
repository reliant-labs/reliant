// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// HistoryLimitRestartMessage is what the user is told when their chat exceeded
// Temporal's per-execution history limit and was restarted from its last
// checkpoint.
//
// It says what happened, that the conversation is intact, and what to expect —
// because the alternative (what happens today) is a chat that silently stops
// responding and a "send a message" that produces one reply and dies again.
const HistoryLimitRestartMessage = "Wow this is a long workflow! This conversation grew long enough to exceed our workflow engine's limit, so it was restarted from its last checkpoint. Your full message history is intact and the assistant continues from where it left off. Any pending or exeucting tasks might need to be redone. Any prior, completed work is safe."

// notifyHistoryLimitRestart tells the user their chat hit the engine's history
// limit and was restarted.
//
// Temporal TERMINATES a run that exceeds the limit — it does not fail in our
// code — so no error path of ours runs and nothing is emitted. Observed on a
// real chat: the run died at 51,201 events, the UI showed no reason, and the
// chat simply appeared frozen. This is the only place that can explain it.
//
// Best-effort: a failed notification must never block the restart that actually
// recovers the chat.
func (s *ChatService) notifyHistoryLimitRestart(ctx context.Context, chatID, workflowID, thread string) {
	errorID := uuid.New().String()
	errorData := map[string]interface{}{
		"update_type":   "error",
		"id":            errorID,
		"chat_id":       chatID,
		"activity_type": "history_limit_restart",
		"activity_id":   workflowID,
		"error_message": HistoryLimitRestartMessage,
		"error_summary": HistoryLimitRestartMessage,
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"workflow_id":   workflowID,
	}
	if thread != "" {
		errorData["thread"] = thread
	}

	payload, err := json.Marshal(errorData)
	if err != nil {
		logging.Warn("Failed to marshal history-limit notice", "chatID", chatID, "error", err)
		return
	}
	if err := s.database.CreateChatUpdate(ctx, chatID, db.UpdateTypeError, errorID, string(payload)); err != nil {
		logging.Warn("Failed to emit history-limit notice", "chatID", chatID, "error", err)
	}
}

// saveIncomingMessages atomically persists the system and user messages a send
// contributes to a thread. It deliberately does not inspect or drain the agent
// mailbox: call_llm is the sole mailbox deliverer, immediately before reading
// thread history, regardless of whether this send starts or resumes the run.
func (s *ChatService) saveIncomingMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	thread, workflowID string,
	systemMessages []*reliantv1.InputMessage,
	userContent string,
	hasUserContent bool,
) (string, error) {
	var savedMessageID string
	err := s.database.RunTx(ctx, func(txCtx context.Context) error {
		for _, sysMsg := range systemMessages {
			if _, err := s.database.SaveMessageToThread(txCtx, req.Msg.ChatId, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle)); err != nil {
				return fmt.Errorf("failed to save system message: %w", err)
			}
		}

		if hasUserContent || len(req.Msg.Attachments) > 0 {
			savedMsg, err := s.database.SaveMessageToThread(txCtx, req.Msg.ChatId, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
			if err != nil {
				return fmt.Errorf("failed to save message: %w", err)
			}
			savedMessageID = savedMsg.ID
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return savedMessageID, nil
}

// liveRunStateUpdate builds the input update a send carries to the chat's run
// and validates it, returning a refusal for the caller to return as-is. Nil
// when the send carries no params or presets.
//
// Every caller runs it BEFORE the send writes anything, because a refused send
// has to be one that did not happen. Prod chat 66a045ce (2026-10-10) answered
// "continue" on a paused run and was refused for its model only after the
// message was saved: the transcript gained a turn no run would read, the
// client was told the send failed, and the retry saved the message a second
// time. #685 stopped refusing that model (one no connected provider can serve
// now moves to one that can), but what is still refused — an unknown model, a
// malformed selector, a user with no provider, a missing required input — was
// refused the same way, after the write, on every path.
//
// The send-time model fallback (fallBackFromUnservableModels) runs after the
// write, not here: it posts a notice, and validation does not depend on it —
// a model it would move is not a validation error.
//
// chat.SelectedPresets must already hold this send's presets (see
// applyRequestPresets), so the update is built from the presets the run will
// actually get.
func (s *ChatService) liveRunStateUpdate(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName string,
	req *connect.Request[reliantv1.SendMessageRequest],
) (map[string]interface{}, error) {
	if len(req.Msg.WorkflowParams) == 0 && len(req.Msg.SelectedPresets) == 0 {
		return nil, nil
	}
	stateUpdate := s.launcher().BuildStateUpdateForActiveWorkflow(ctx, userID, chat, workflowName, req.Msg.SelectedPresets, req.Msg.WorkflowParams)
	if validationErrors := s.launcher().ValidateWorkflowInputs(ctx, userID, workflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
		return nil, inputValidationError(validationErrors)
	}
	return stateUpdate, nil
}

// inputValidationError is SendMessage's refusal for inputs that failed
// validation: InvalidArgument, naming every failure.
func inputValidationError(validationErrors []error) error {
	errMsgs := make([]string, len(validationErrors))
	for i, e := range validationErrors {
		errMsgs[i] = e.Error()
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
}

// applyRequestPresets makes a send's preset selection the chat's, in memory
// only, and reports whether it did. The caller persists it with
// persistChatPresets once the send has passed validation.
func applyRequestPresets(chat *db.Chat, requestPresets map[string]string) bool {
	if len(requestPresets) == 0 {
		return false
	}
	chat.SelectedPresets = requestPresets
	return true
}

// persistChatPresets writes the chat's preset selection. Best-effort: the
// presets also travel in the run's inputs, so a failed write costs only the
// selection shown next time.
func (s *ChatService) persistChatPresets(ctx context.Context, chat *db.Chat) {
	chat.UpdatedAt = time.Now().UTC()
	if err := s.database.UpdateChat(ctx, chat); err != nil {
		logging.Error("Failed to update chat presets", "error", err, "chatID", chat.ID)
	}
}

// markResumeAnswer wraps a plain user message that is being delivered as a
// question answer to RESUME a canceled/failed workflow (the reset-and-replay
// recovery path), so the resumed LLM knows its tool-call "answer" is actually a
// post-failure resume, not a direct answer. A normal live form-answer to a
// still-running workflow is NOT wrapped. The format is a fixed contract.
func markResumeAnswer(userMessage string) string {
	return "<system> workflow was canceled or failed. user resumed with message</system>: " + userMessage
}

// questionResumeResponseData builds the response_data delivered to a parked
// ask_question's signal so the workflow's parseQuestionResponse reads answer as
// the answer's freetext (feedback). Used only for the plain-message resume path.
func questionResumeResponseData(answer string) (string, error) {
	payload := map[string]interface{}{
		"answers": []map[string]interface{}{
			{"question": "", "selected": []string{}, "freetext": answer},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// resumeFailedQuestionWorkflow resumes a Failed/Terminated workflow that died
// while parked on an unanswered ask_question, by delivering the user's plain
// message as the (marked) question answer. PauseService.SignalWithRecovery
// reset-replays the dead run and re-sends signal.question.<id> on the new run,
// so the rebuilt run re-parks on the same question channel and receives it —
// preserving nested inline state (loop iteration, active sub-threads) that the
// coarse restart would lose.
//
// Returns (response, resumed, presavedMessageID). When resumed is false the
// caller falls back to the coarse restart; the question has already been
// resolved and the message saved (presavedMessageID), so the caller reuses it.
//
// inputUpdate reaches the replayed run before the answer does.
func (s *ChatService) resumeFailedQuestionWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	chat *db.Chat,
	existingWorkflow *db.Workflow,
	question *db.Question,
	targetThread, userID, userContent string,
	systemMessages []*reliantv1.InputMessage,
	inputUpdate map[string]interface{},
) (*connect.Response[reliantv1.SendMessageResponse], bool, string) {
	workflowID := existingWorkflow.ID

	// The delivered answer carries the resume marker; the raw message is the
	// user's text. Both go into the thread/answer so the resumed LLM sees the
	// resume context.
	marked := markResumeAnswer(userContent)
	responseData, err := questionResumeResponseData(marked)
	if err != nil {
		logging.Error("Failed to build question resume response data", "error", err, "questionID", question.ID)
		return nil, false, ""
	}

	// Resolve the DB question (so it is no longer pending regardless of outcome)
	// and persist the messages so the resumed run reads them at its next LLM
	// boundary.
	if err := s.database.ResolveQuestion(ctx, question.ID, &responseData); err != nil {
		logging.Warn("Failed to resolve question during resume", "error", err, "questionID", question.ID)
	}
	if err := s.database.EmitQuestionUpdate(ctx, question.ChatID, db.QuestionUpdate{
		QuestionID: question.ID,
		ChatID:     question.ChatID,
		WorkflowID: question.WorkflowID,
		StepID:     question.StepID,
		Status:     "resolved",
	}); err != nil {
		logging.Warn("Failed to emit question update during resume", "error", err, "questionID", question.ID)
	}

	// Persist to the QUESTION's thread — the thread the parked ask_question (and
	// the resumed run's next LLM call within it) reads — so the marker reaches
	// the LLM. For a nested/forked ask loop this is the sub-thread, not the root.
	answerThread := question.ThreadID
	if answerThread == "" {
		answerThread = targetThread
	}
	// The answer is saved through saveIncomingMessages, which writes it in one
	// transaction and leaves the mailbox alone: anything queued for this thread
	// stays queued, and the resumed run's next call_llm delivers it before it
	// reads history. Save failures stay non-fatal here (the question is already
	// resolved and blocking the resume helps nobody); a failed save writes
	// nothing, and the queued rows are untouched either way.
	presavedID, saveErr := s.saveIncomingMessages(ctx, req, answerThread, workflowID, systemMessages, marked, true)
	if saveErr != nil {
		logging.Warn("Failed to save resume messages during question resume", "error", saveErr)
	}

	// Deliver the answer. The run service reset-replays the dead run (honoring
	// the reset guard), re-sends the signal on the new run, and refreshes the
	// chat's run id.
	outcome, err := s.runs.ResumeViaSignal(ctx, runs.ResumeViaSignalInput{
		ChatID:           req.Msg.ChatId,
		WorkflowID:       workflowID,
		TargetWorkflowID: question.TemporalWorkflowID,
		SignalName:       "signal.question." + question.ID,
		SignalData: map[string]interface{}{
			"status":        "resolved",
			"response_data": responseData,
		},
		InputUpdate: inputUpdate,
	})
	if err != nil || outcome.Kind != runs.OutcomeResumed {
		logging.Info("Question-parked workflow not reset-resumable - coarse restart at position",
			"chatID", req.Msg.ChatId, "workflowID", workflowID, "questionID", question.ID, "error", err)
		return nil, false, presavedID
	}
	newRunID := outcome.RunID

	logging.Info("Question-parked workflow reset-and-resumed via answer (precise nested resume)",
		"chatID", req.Msg.ChatId, "workflowID", workflowID, "questionID", question.ID, "newRunID", newRunID)

	workflowStatus := fmt.Sprintf("%d", db.Active())
	go s.trackMessageSent(ctx, userID, chat, presavedID, targetThread, userContent, len(req.Msg.Attachments))
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		RunId:          newRunID,
		Status:         "processing",
		WorkflowStatus: &workflowStatus,
		MessageId:      presavedID,
	}), true, presavedID
}

// resurrectGhostWorkflow handles the case where a workflow exists in DB as running/paused
// but is missing from Temporal (ghost workflow). Instead of failing, we restart the workflow
// with a fresh Temporal execution, allowing the conversation to continue seamlessly.
//
// This is a defensive recovery mechanism that ensures the system always strives toward
// a working state, even after Temporal data loss or server restarts.
func (s *ChatService) resurrectGhostWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	chat *db.Chat,
	existingWorkflow *db.Workflow,
	workflowID string,
	userID string,
) (*connect.Response[reliantv1.SendMessageResponse], error) {
	// Restart the workflow the CHAT currently points at, not the (possibly
	// stale) db.Workflow ROW name. After a transition_to handoff the row still
	// records the completed one-shot pipeline while chat.WorkflowName holds the
	// target the conversation moved to; resurrecting the row name would re-run
	// e.g. forge-one-shot on an already-built project.
	workflowName := activeWorkflowNameForResume(chat, existingWorkflow)

	// Extract user and system messages from input
	userContent, systemMessages, hasUserContent := extractMessagesFromInput(req.Msg.Messages)

	// The request's presets replace the chat's; persisted once the inputs
	// below pass validation.
	presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)

	// Determine target thread - default to root workflow thread
	targetThread := existingWorkflow.Thread
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// Step 1: Build workflow options - same ID, fresh execution
	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}

	// Step 2: Build workflow inputs (with presets and model defaults)
	// Use worktree path if available, otherwise project path
	checkout := s.launcher().GetEffectiveCheckout(ctx, chat)

	// Merge presets: existing chat presets + any new ones from the request
	effectivePresets := make(map[string]string)
	for k, v := range chat.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}
	for k, v := range req.Msg.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}

	initialData := s.launcher().BuildWorkflowInputs(ctx, userID, checkout, chat.ProjectID, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Validate workflow inputs before starting, and before anything of the
	// send is written — a refused send leaves nothing behind (see
	// liveRunStateUpdate).
	if validationErrors := s.launcher().ValidateWorkflowInputs(ctx, userID, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		return nil, inputValidationError(validationErrors)
	}
	if presetsChanged {
		s.persistChatPresets(ctx, chat)
	}

	// Step 3: Save messages BEFORE starting workflow. The DB said running but
	// Temporal lost the execution. Anything queued in this thread's mailbox is
	// left there: the resurrected run's first call_llm delivers it.
	savedMessageID, err := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
	if err != nil {
		logging.Error("[Ghost Recovery] Failed to save messages", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.fallBackFromUnservableModels(ctx, userID, req.Msg.ChatId, chat.ProjectID, workflowName, workflowID, targetThread, initialData, nil)

	// Step 4: Build execution context - inheriting existing thread
	// We use ThreadModeInherit because we're continuing an existing conversation
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Thread:       targetThread,
		ThreadMode:   model.ThreadModeInherit, // Inherit existing thread context
	}
	if jwt, ok := auth.GetUserJWT(userID); ok {
		execContext.UserJWT = jwt
	}

	// Note: Message was already saved above before ghost recovery

	// Inject session daemon if set on chat
	launch.InjectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.database, req.Msg.ChatId),
		// A ghost (Temporal lost the running execution) is an infra failure,
		// not user intent — the fresh execution resumes at position.
		Resume: v2.ResumeInputFromDurableState(ctx, s.database, req.Msg.ChatId, workflowID),
	}

	// Step 5: Start fresh Temporal execution
	workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("[Ghost Recovery] Failed to start workflow", "error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resurrect workflow"))
	}

	runID := workflowRun.GetRunID()
	logging.Info("[Ghost Recovery] Successfully resurrected workflow",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"newRunID", runID,
		"workflowName", workflowName,
		"thread", targetThread,
	)

	// Step 6: Update run IDs and workflow status
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)
	if err := s.database.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
		logging.Warn("[Ghost Recovery] Failed to update workflow status", "error", err, "workflowID", workflowID)
		// Non-fatal - workflow is running in Temporal
	}

	workflowStatus := fmt.Sprintf("%d", db.Active())
	go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		RunId:          runID,
		Status:         "processing",
		WorkflowStatus: &workflowStatus,
		MessageId:      savedMessageID,
	}), nil
}

// SendMessage sends messages to a chat and continues workflow
func (s *ChatService) SendMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
) (*connect.Response[reliantv1.SendMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Extract user and system messages from input
	userContent, systemMessages, hasUserContent := extractMessagesFromInput(req.Msg.Messages)

	// Require at least one user message or attachments
	if !hasUserContent && len(req.Msg.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one user message or attachment is required"))
	}

	// Get chat with ownership check (defense-in-depth via single query)
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		logging.Error("Failed to get chat", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	s.wakeDaemonForAttendedTurn(ctx, userID, chat)

	if chat.Activity != nil && reliantv1.ChatActivity(*chat.Activity) == reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON {
		// A live run is waiting for the machine this send just woke. Its
		// marker still describes it — clearing it would show the run as
		// thinking until its next check, which can be minutes away — so ask
		// the run to check now instead: it clears the marker itself once the
		// machine is up. A run held mid-turn does not listen, and its next
		// tool call settles the marker the same way.
		s.nudgeMachineWait(ctx, chat)
	} else if err := s.database.SetChatDaemonBlocked(ctx, chat.ID, false); err != nil {
		// An attended send wakes the machine, so a daemon-pending marker left
		// by an earlier run no longer describes it. The next tool call re-sets
		// it if the machine is still unreachable.
		logging.Warn("Failed to clear daemon-pending marker on send", "error", err, "chatID", chat.ID)
	}
	// A message queued for the machine is read by whichever run this send
	// starts, resumes or wakes — the run reads the thread, where it already
	// is — so it is no longer waiting on the machine to be delivered. If that
	// run's machine is still down, its preflight queues both again.
	if _, err := s.database.SetChatQueuedForMachine(ctx, chat.ID, false); err != nil {
		logging.Warn("Failed to clear queued-for-machine marker on send", "error", err, "chatID", chat.ID)
	}

	// Note: Previously checked if workflow completed and thread is closed.
	// Removed to allow restarting workflows - SendMessage will start a new workflow
	// for completed/failed/cancelled workflows (see status switch below).

	if err := launch.ValidateWorkflowParamStructure(req.Msg.WorkflowParams); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Resume-at-position state, populated when the prior run for this chat was
	// interrupted (failed/terminated/lost) rather than completed or
	// user-cancelled. Decision table for the new-run start below:
	//   failed / terminated / wedged  -> reset-and-replay (precise) when Temporal
	//                                     has replayable history; else coarse
	//                                     resume at checkpointed position
	//   completed                     -> fresh start
	//   user-cancelled                -> fresh start (thread history only)
	var resumeInput *v2.ResumeInput
	var resumeThread string
	// Set when a resume branch already persisted the incoming messages (e.g. the
	// reset-and-replay path saves them before reset, then falls back to coarse
	// restart) so the new-run flow below does not double-save them.
	var resumeMessagesSaved bool
	var resumePresavedMessageID string
	// Set when the run this message was saved to closed before it could be
	// woken: the fresh run started for the message serves the thread it was
	// saved to.
	var lateRunThread string

	// The status read below decides between resuming a paused run and waking a
	// running one, and that decision is only right if no pause or resume is
	// half-applied while it is made and delivered. PauseChat signals Temporal
	// before it writes the paused status; a send that read the row in that gap
	// saw "running", only rang the thread-wake doorbell, and left the run
	// paused with the message unread (chat 264b5697, stuck 4.5 minutes). So
	// the read and everything it routes to run inside the run-control critical
	// section. See runs.Service.LockRunControl.
	releaseRunControl := s.runs.LockRunControl(ctx, req.Msg.ChatId)
	defer releaseRunControl()

	// Check workflow status to decide: resume paused, send to running, or start
	// new. The transaction below only reads; what keeps the decision from going
	// stale is the run-control lock held above, not the transaction.
	if workflowID := chat.MainThreadID(); workflowID != "" {
		var existingWorkflow *db.Workflow

		err := s.database.RunTx(ctx, func(txCtx context.Context) error {
			wf, err := s.database.GetWorkflow(txCtx, workflowID)
			if err != nil {
				return err
			}
			existingWorkflow = wf
			return nil
		})

		if err == nil && existingWorkflow != nil {
			// Use Temporal as source of truth for workflow status
			// For running workflows, verify Temporal agrees
			if existingWorkflow.Status == db.Active() {
				temporalState, temporalErr := s.runs.State(ctx, workflowID)
				if temporalErr != nil {
					// Temporal query failed - this is an error we should surface
					logging.Error("Failed to query Temporal for workflow status",
						"error", temporalErr,
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
					)
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", temporalErr))
				}

				if !temporalState.Exists {
					// Ghost workflow detected - Temporal lost the workflow but DB says running
					// RESURRECT: Start a fresh Temporal execution with the same workflow ID
					// This allows the conversation to continue seamlessly
					logging.Info("[Ghost Recovery] Resurrecting ghost workflow",
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
						"dbStatus", existingWorkflow.Status,
						"workflowName", existingWorkflow.WorkflowName,
					)

					return s.resurrectGhostWorkflow(ctx, req, chat, existingWorkflow, workflowID, userID)
				} else if !temporalState.IsRunning {
					if existingWorkflow.Status == db.Paused() {
						// Paused workflow whose Temporal execution timed out or completed.
						// Keep status as paused — the paused handler uses PauseService.ResumeWorkflow
						// which handles reset-based resume for expired executions.
						logging.Info("Paused workflow Temporal execution not running - will attempt reset-based resume",
							"workflowID", workflowID,
							"chatID", req.Msg.ChatId,
							"temporalStatus", temporalState.Status,
						)
					} else {
						// Running workflow that stopped — reconcile status
						s.runs.Reconcile(ctx, workflowID, existingWorkflow.Status, temporalState.Status)
						existingWorkflow.Status = temporalState.Status
					}
				}
			}

			switch existingWorkflow.Status {
			case db.Pending():
				// Never started (a branch awaiting its first send). The first
				// send is StartChat's, which records the launch exactly once;
				// letting SendMessage start it would bypass that.
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Errorf("chat has not started; call StartChat"))

			case db.Paused():
				// Refuse bad params before anything is written; see
				// liveRunStateUpdate.
				presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)
				stateUpdate, err := s.liveRunStateUpdate(ctx, userID, chat, existingWorkflow.WorkflowName, req)
				if err != nil {
					return nil, err
				}
				if presetsChanged {
					s.persistChatPresets(ctx, chat)
				}

				// Determine target thread - use workflow's thread from DB (not root workflow ID)
				// to handle forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save the messages in one transaction. The mailbox is not
				// touched: a paused run delivers anything queued for the thread
				// at its next call_llm, once it resumes.
				//
				// The workflow-status flip is deliberately NOT in here.
				// Postgres and Temporal cannot commit together, so the only
				// question is which order fails safely, and "mark running, then
				// signal" is the unsafe one: if the DB write fails, this
				// returned an error before ever reaching ResumeWorkflow below
				// and the run stayed parked with nobody coming for it — visibly
				// "active" while actually halted, and invisible to the
				// reconciler, whose progress watchdog skips paused rows by
				// design. That is exactly what a serialization failure did to
				// chat 80978aca: paused 20:52, resume aborted 20:55, stuck
				// until a later message happened to retry it at 21:29.
				//
				// Signalling first inverts the failure: the run really is
				// awake, and the status is corrected by whichever of three
				// writers gets there — the best-effort write below, the
				// workflow's own "started"/Resumed notification when it wakes
				// (see the retry-exhaustion paths in loop_executor.go and
				// inline_workflow_executor.go), or the reconciler. A stale
				// "paused" row on a running workflow is self-healing; a
				// never-signalled workflow is not.
				var savedMessageID string
				err = s.database.RunTx(ctx, func(txCtx context.Context) error {
					var saveErr error
					savedMessageID, saveErr = s.saveIncomingMessages(txCtx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
					return saveErr
				})
				if err != nil {
					// The message itself failed to save — there is nothing to
					// resume the workflow FOR, so this really is fatal.
					logging.Error("Failed to save messages while resuming paused workflow",
						"error", err, "workflowID", workflowID, "chatID", req.Msg.ChatId)
					return nil, connect.NewError(connect.CodeInternal, err)
				}

				// Get RunID from chat (workflow struct doesn't have it)
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				// The input update the run resumes with: the param/preset
				// update validated above and the run's own model pin, each
				// moved off a provider that cannot serve it (#685) — even
				// when this send carries no model at all (resumeInputUpdate).
				runInputs := s.runInputsForSend(ctx, workflowID, runID)
				inputUpdate, modelNotices := s.resumeInputUpdate(ctx, userID, req.Msg.ChatId, chat.ProjectID, existingWorkflow.WorkflowName, workflowID, stateUpdate, runInputs)

				// Only add "params changed" message if params actually changed from current workflow state
				if len(inputUpdate) > 0 && (runInputs == nil || inputsDiffer(runInputs, inputUpdate)) {
					hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
					_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
					if err != nil {
						logging.Warn("Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
					}
				}

				// Resume the run. The service signals a live execution,
				// reset-replays a dead-but-replayable one, and refreshes the
				// chat's run id when a reset minted a new one — and whichever
				// run wakes gets the input update first.
				outcome, resumeErr := s.runs.Resume(ctx, req.Msg.ChatId, inputUpdate)
				if resumeErr != nil {
					// The resume is the step that actually restarts the run.
					// Swallowing this failure and returning
					// workflow_status=running below is what makes a chat look
					// active while it is halted: the message is saved, the UI
					// shows work in progress, and nothing is executing. Report
					// it instead — the user's message is already durable, so a
					// retry resumes from exactly here.
					logging.Error("Failed to resume paused workflow",
						"error", resumeErr, "workflowID", workflowID, "chatID", req.Msg.ChatId)
					return nil, connect.NewError(connect.CodeInternal,
						fmt.Errorf("failed to resume paused workflow: %w", resumeErr))
				}
				if outcome.Kind != runs.OutcomeResumed {
					logging.Warn("Paused workflow could not be resumed in place during SendMessage - resuming at position with a new run",
						"workflowID", workflowID, "chatID", req.Msg.ChatId)
					// The paused run was lost (infra failure, not user
					// intent) — start a new run that resumes at position.
					resumeInput = v2.ResumeInputFromDurableState(ctx, s.database, req.Msg.ChatId, workflowID)
					resumeThread = existingWorkflow.Thread
					// Fall through to start a new workflow below
					break
				}
				// Told only now: the run that has the moved models is awake.
				// A fresh run (above) makes and announces its own.
				s.postModelFallbackNotices(ctx, req.Msg.ChatId, workflowID, targetThread, modelNotices)

				// The run is awake. Correct the DB status now that the
				// authoritative step has succeeded. Best-effort on purpose: if
				// this write loses a serialization race, the workflow's own
				// "started"/Resumed notification and the reconciler both still
				// converge it, and a stale row is not worth failing a request
				// that already did the important part.
				if err := s.database.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
					logging.Warn("Resumed workflow but failed to mark it running — status will be corrected by the workflow or the reconciler",
						"error", err, "workflowID", workflowID, "chatID", req.Msg.ChatId)
				}

				if outcome.RunID != "" {
					runID = outcome.RunID
				}

				// Wake the target thread, for the same reason the running
				// branch does. Resuming is NOT sufficient on its own:
				// broadcastResume clears the pause gate, but a thread parked
				// in awaitLiveDetachedSpawns is blocked on a different Await
				// whose predicate never looks at the pause epoch. That is
				// exactly what happened on chat 7da3935c — the 21:37:46
				// resume woke the spawn's loop and left the root thread
				// parked with the user's message unread.
				_ = s.notifyThreadWake(ctx, chat, targetThread, threadwake.ReasonUserMessage)

				// Return with updated workflow_status so frontend knows we resumed
				workflowStatus := fmt.Sprintf("%d", db.Active())
				go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      savedMessageID,
				}), nil

			case db.Active():
				// Refuse bad params before anything is written or queued; see
				// liveRunStateUpdate.
				presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)
				stateUpdate, err := s.liveRunStateUpdate(ctx, userID, chat, existingWorkflow.WorkflowName, req)
				if err != nil {
					return nil, err
				}
				if presetsChanged {
					s.persistChatPresets(ctx, chat)
				}

				// Use workflow's thread from DB (not root workflow ID) to handle
				// forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save system messages first
				for _, sysMsg := range systemMessages {
					_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
					if err != nil {
						logging.Error("Failed to save system message to running workflow", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message: %w", err))
					}
				}

				// Queue the user message for the thread's next turn rather than
				// writing it into history now. A running thread may be mid-turn,
				// and its reply is saved only when its stream ends: a message
				// written meanwhile takes the earlier seq and sits BEFORE the
				// reply that never saw it. History then ends with the assistant,
				// and the turn the wake buys yields instead of answering — the
				// message is in the transcript and never answered. The mailbox is
				// the one delivery point that lands input at a turn boundary, in
				// order (call_llm drains it immediately before it reads history),
				// so a message to a running thread goes there, exactly as the
				// composer's queue does.
				var queuedMsg *db.AgentMessage
				if hasUserContent || len(req.Msg.Attachments) > 0 {
					queuedMsg = &db.AgentMessage{
						// The client's own id for the message, so the copy it
						// is already showing and the queued row are one item.
						ID:           clientMessageID(req.Msg.GetClientMessageId()),
						FromThreadID: chat.MainThreadID(),
						ChatID:       req.Msg.ChatId,
						ToThreadID:   targetThread,
						Kind:         core.AgentMessageKindHumanMessage,
						Body:         userContent,
						Attachments:  req.Msg.Attachments,
						Status:       core.AgentMessageStatusQueued,
						CreatedAt:    time.Now(),
					}
					if err := s.database.EnqueueAgentMessage(ctx, queuedMsg); err != nil {
						logging.Error("Failed to queue message for running workflow", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save message: %w", err))
					}
				}

				// Signal workflow with the param/preset input update validated
				// above, after the send-time model policy (#685) has moved any
				// model no connected provider can serve.
				if len(stateUpdate) > 0 {
					runID := ""
					if chat.RunID != nil {
						runID = *chat.RunID
					}
					s.fallBackFromUnservableModels(ctx, userID, req.Msg.ChatId, chat.ProjectID, existingWorkflow.WorkflowName, workflowID, targetThread, stateUpdate, func(input string) bool {
						return s.checkParamsActuallyChanged(ctx, workflowID, runID, map[string]interface{}{input: stateUpdate[input]})
					})

					// Only add "params changed" message if params actually changed from current workflow state
					if s.checkParamsActuallyChanged(ctx, workflowID, runID, stateUpdate) {
						hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
						_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
						if err != nil {
							logging.Error("Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
							// Non-fatal, continue to signal
						}
					}

					if err := s.tempClient.SignalWorkflow(ctx, workflowID, runID, "update_workflow_state", stateUpdate); err != nil {
						logging.Warn("Failed to signal workflow with param updates", "error", err, "workflowID", workflowID)
					}
				}

				// Get RunID from chat
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				// Wake the target thread. The wake is the run's only record
				// that this message exists, because a running workflow is not
				// necessarily a turn that will read it:
				//   - a thread that has fanned work out to background spawns
				//     is parked in awaitLiveDetachedSpawns, and takes no turn
				//     until something wakes it (chat 7da3935c: the root thread
				//     ignored the user while its one live spawn ran);
				//   - a thread on its LAST turn has already checked its
				//     mailbox, and without the wake would end the run with
				//     this message queued. The loop-exit gate and the
				//     end-of-run check both read the wake and give it a turn
				//     (late_wake.go).
				//
				// And the run may have closed since the status read above. A
				// wake that reaches no run starts one for the message — so it
				// is never left for the user's next send to discover.
				if queuedMsg != nil && s.wakeLiveRun(ctx, chat, targetThread, threadwake.ReasonMailbox) {
					logging.Info("Run finished before a message to it could wake it; starting a run for the message",
						"chatID", req.Msg.ChatId, "workflowID", workflowID, "threadID", targetThread)
					resumeMessagesSaved = true
					resumePresavedMessageID = queuedMsg.ID
					lateRunThread = targetThread
					break
				}

				workflowStatus := fmt.Sprintf("%d", db.Active())
				// The queued row's ID: the message enters history when the
				// thread's next turn drains it.
				var messageID string
				if queuedMsg != nil {
					messageID = queuedMsg.ID
				}
				go s.trackMessageSent(ctx, userID, chat, messageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      messageID,
					Queued:         queuedMsg != nil,
				}), nil

			// The EXPIRED resume branch that used to sit here is gone with the
			// status it matched: nothing ever wrote EXPIRED (Temporal
			// TIMED_OUT is recorded as a failure), so this arm was
			// unreachable. A timed-out run now takes the FAILED arm below,
			// which is where it was already being routed in practice.

			case db.Failed():
				// Stuck (database says failed while Temporal says running)
				// cannot be restarted - user must branch to continue.
				inspection, inspectErr := s.runs.Inspect(ctx, req.Msg.ChatId)
				if inspectErr != nil {
					logging.Error("Failed to inspect run", "error", inspectErr, "chatID", req.Msg.ChatId)
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", inspectErr))
				}
				if inspection.Stuck {
					return nil, connect.NewError(connect.CodeFailedPrecondition,
						fmt.Errorf("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message"))
				}

				// Both recoveries below save the message before they know
				// whether they can serve it, so bad params are refused here,
				// first; see liveRunStateUpdate. The run they restart gets the
				// same inputs, so the same check applies (in memory only: the
				// new-run path below persists the presets once it starts).
				applyRequestPresets(chat, req.Msg.SelectedPresets)
				stateUpdate, err := s.liveRunStateUpdate(ctx, userID, chat, activeWorkflowNameForResume(chat, existingWorkflow), req)
				if err != nil {
					return nil, err
				}

				// What a reset-and-replay hands the replayed run, before it
				// wakes. Replay rebuilds the run from the inputs its history
				// recorded, so without this a model the user just picked —
				// or a pin no connected provider can serve, which the send
				// moves (resumeInputUpdate) — never reaches it. The coarse
				// restart below builds its own inputs from this send.
				var inputUpdate map[string]interface{}
				var modelNotices []launch.ModelSubstitution
				if inspection.Recoverable {
					inputUpdate, modelNotices = s.resumeInputUpdate(ctx, userID, req.Msg.ChatId, chat.ProjectID, existingWorkflow.WorkflowName, workflowID,
						stateUpdate, s.runInputsForSend(ctx, workflowID, ""))
				}

				// A run that died parked on an unanswered ask_question wakes on
				// signal.question.<id>, not signal.resume. Deliver the user's plain
				// message as the (marked) question answer via reset-and-replay so it
				// resumes PRECISELY (the rebuilt run re-parks on the same question
				// channel and receives it) instead of coarse-restarting the loop.
				var pendingQuestion *db.Question
				if inspection.Recoverable {
					pendingQuestion, _ = s.database.GetPendingQuestionByChatID(ctx, req.Msg.ChatId)
				}
				if pendingQuestion != nil && pendingQuestion.TemporalWorkflowID != "" && hasUserContent {
					targetThread := existingWorkflow.Thread
					if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
						targetThread = *req.Msg.TargetThread
					}
					resp, resumed, presavedID := s.resumeFailedQuestionWorkflow(ctx, req, chat, existingWorkflow, pendingQuestion, targetThread, userID, userContent, systemMessages, inputUpdate)
					if resumed {
						s.postModelFallbackNotices(ctx, req.Msg.ChatId, workflowID, targetThread, modelNotices)
						return resp, nil
					}
					// Guard-exhausted / not reset-resumable: the question is already
					// resolved and messages saved — fall through to coarse restart.
					resumeMessagesSaved = true
					resumePresavedMessageID = presavedID
					resumeInput = v2.ResumeInputFromDurableState(ctx, s.database, req.Msg.ChatId, workflowID)
					resumeThread = existingWorkflow.Thread
					break
				}

				// Failed/terminated/wedged run with a CLOSED Temporal execution
				// that still has replayable history: prefer RESET-AND-REPLAY. The
				// new run replays the recorded history, which rebuilds the entire
				// (possibly nested) engine stack — so a run that died
				// mid-nested-get-it-right resumes at the SAME review iteration
				// with reviewer feedback intact, instead of the coarse
				// flat-checkpoint restart that can only re-enter a TOP-LEVEL node
				// and restarts the nested loop at iteration 0. We fall back to the
				// coarse restart only when Temporal has nothing to replay (ghost),
				// the bounded guard has given up (deterministic failure), or the run
				// was parked on an unanswered question (handled above).
				if inspection.Recoverable && pendingQuestion == nil {
					targetThread := existingWorkflow.Thread
					if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
						targetThread = *req.Msg.TargetThread
					}
					// Persist messages BEFORE reset so the resumed run reads
					// them, mailbox first: the interrupted run never reached
					// another drain boundary, so whatever was queued for this
					// thread still belongs ahead of the new message.
					presavedID, saveErr := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
					if saveErr != nil {
						return nil, connect.NewError(connect.CodeInternal, saveErr)
					}
					resumeMessagesSaved = true
					resumePresavedMessageID = presavedID

					outcome, resumeErr := s.runs.ResumeInterrupted(ctx, req.Msg.ChatId, inputUpdate)
					if resumeErr != nil {
						return nil, connect.NewError(connect.CodeInternal, resumeErr)
					}
					if outcome.Kind == runs.OutcomeResumed {
						s.postModelFallbackNotices(ctx, req.Msg.ChatId, workflowID, targetThread, modelNotices)
						workflowStatus := fmt.Sprintf("%d", db.Active())
						go s.trackMessageSent(ctx, userID, chat, presavedID, targetThread, userContent, len(req.Msg.Attachments))
						return connect.NewResponse(&reliantv1.SendMessageResponse{
							ChatId:         req.Msg.ChatId,
							WorkflowId:     workflowID,
							RunId:          outcome.RunID,
							Status:         "processing",
							WorkflowStatus: &workflowStatus,
							MessageId:      presavedID,
						}), nil
					}
					if outcome.HistoryLimitExceeded {
						// Resetting cannot help a run at the history cap, so
						// this goes to the coarse fresh restart — and the user
						// is told, because otherwise the chat simply stops with
						// no explanation and "send a message" appears to do
						// nothing.
						s.notifyHistoryLimitRestart(ctx, req.Msg.ChatId, workflowID, targetThread)
					}
					// Messages already saved; fall through to the coarse restart.
				}

				// Coarse resume-at-position: the new run enters directly at the
				// checkpointed node instead of graph entry, so entry routers never
				// re-classify the user's "continue" message. Used when there is no
				// replayable history (ghost) or the reset guard gave up.
				resumeInput = v2.ResumeInputFromDurableState(ctx, s.database, req.Msg.ChatId, workflowID)
				resumeThread = existingWorkflow.Thread
				logging.Info("Interrupted workflow detected - new run will resume at position",
					"chatID", req.Msg.ChatId,
					"workflowID", workflowID,
					"checkpointNode", resumeInput.NodeID,
					"loopIteration", resumeInput.LoopIteration,
				)

			case db.Cancelled(), db.Completed():
				// User-cancelled or completed: start a fresh workflow at graph
				// entry (thread history is still the conversation context).
				// Fall through to normal flow.
			}
		}
	}

	// Workflow name is always set on chat (required at creation)
	workflowName := *chat.WorkflowName

	// A chat with no root workflow has never started; that first send is
	// StartChat's.
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("chat has not started; call StartChat"))
	}

	// A started chat's workflow is fixed. Switching is only possible while a
	// chat is pending, and StartChat owns that.
	if req.Msg.Workflow != nil && *req.Msg.Workflow != "" && *req.Msg.Workflow != workflowName {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("cannot change workflow after chat has started - use Branch to create a new chat with a different workflow"))
	}

	// The request's presets replace the chat's. Applied in memory here, and
	// persisted only once the inputs below have passed validation.
	presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)

	workflowOptions := s.newRunOptions(workflowID)

	// Merge presets: chat presets are base, request presets override
	effectivePresets := make(map[string]string)
	for k, v := range chat.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}
	for k, v := range req.Msg.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}

	// Resolve path for preset loading and workflow validation - use worktree if available
	checkout := s.launcher().GetEffectiveCheckout(ctx, chat)

	// Build workflow inputs from merged presets and user params
	initialData := s.launcher().BuildWorkflowInputs(ctx, userID, checkout, chat.ProjectID, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Validate workflow inputs before starting — and before the messages are
	// saved below. This catches missing required inputs, unknown models and a
	// user with no provider early (400), and a refused send must leave nothing
	// behind; see liveRunStateUpdate.
	if validationErrors := s.launcher().ValidateWorkflowInputs(ctx, userID, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		return nil, inputValidationError(validationErrors)
	}
	if presetsChanged {
		s.persistChatPresets(ctx, chat)
	}

	// Determine target thread. Resume runs continue the interrupted run's
	// thread (which may be a forked/child thread) so history stays continuous.
	targetThread := workflowID
	if lateRunThread != "" {
		targetThread = lateRunThread
	}
	threadMode := model.ThreadModeNew
	if resumeInput != nil {
		threadMode = model.ThreadModeInherit
		if resumeThread != "" {
			targetThread = resumeThread
		}
	}
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// Build execution context for the workflow
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Thread:       targetThread,
		ThreadMode:   threadMode,
	}
	if jwt, ok := auth.GetUserJWT(userID); ok {
		execContext.UserJWT = jwt
	}

	// Save messages BEFORE starting workflow for consistency: system messages,
	// then the new user message. Anything still queued in this thread's
	// mailbox is not touched here — the new run's first call_llm delivers it,
	// immediately before it reads history.
	//
	// A resume branch (reset-and-replay fallback) may have already persisted the
	// messages before its reset attempt, and so has a send whose run closed
	// before it could be woken (lateRunThread) — skip re-saving so they aren't
	// doubled.
	var savedMessageID string
	var greenfieldProbe bool
	if resumeMessagesSaved {
		savedMessageID = resumePresavedMessageID
	} else {
		// A chat whose first turn this still is may be opening on a directory
		// with no code, where the stack is undecided. The run finds out and
		// seeds the guidance before its first LLM call; this only decides it
		// is a first turn, and must do so before the message below is saved.
		// A resume continues a conversation, so it never is one.
		greenfieldProbe = resumeInput == nil && s.launcher().WantsGreenfieldProbe(ctx, chat)

		saved, err := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
		if err != nil {
			logging.Error("Failed to save messages for new workflow", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		savedMessageID = saved
	}

	s.fallBackFromUnservableModels(ctx, userID, req.Msg.ChatId, chat.ProjectID, workflowName, workflowID, targetThread, initialData, nil)

	// Inject session daemon if set on chat
	launch.InjectSessionDaemonID(initialData, chat)

	// A message queued to a run that closed before its wake is still in the
	// mailbox, and the closed run left the thread terminal — the shape the
	// reconciler's orphaned-mailbox sweep marks undelivered. Revive the thread
	// BEFORE the run starts, so the sweep can never take the row first; the
	// run's first call_llm delivers it.
	if lateRunThread != "" {
		if _, err := s.database.ReviveThread(ctx, lateRunThread); err != nil {
			logging.Warn("Could not revive the thread a late message was queued to; starting its run anyway",
				"error", err, "chatID", req.Msg.ChatId, "threadID", lateRunThread)
		}
	}

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.database, req.Msg.ChatId),
		Resume:       resumeInput,

		GreenfieldProbe: greenfieldProbe,
	}

	workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("Failed to start workflow", "error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start workflow"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)

	workflowStatus := fmt.Sprintf("%d", db.Active())
	go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		RunId:          runID,
		Status:         "processing",
		WorkflowStatus: &workflowStatus,
		MessageId:      savedMessageID,
		// A run started for a message queued to a run that closed first: the
		// row is still queued, and this run's first turn drains it.
		Queued: lateRunThread != "",
	}), nil
}

// clientMessageID is the id the client chose for its message, when it is one a
// queued row can carry; otherwise a fresh one.
func clientMessageID(clientMessageID string) string {
	if id, err := uuid.Parse(clientMessageID); err == nil {
		return id.String()
	}
	return uuid.New().String()
}

// SendAgentMessage queues a HUMAN message directly into a specific running
// thread's mailbox (agent_messages), without pausing or otherwise touching
// the chat's workflow/pause state. It is the human-facing counterpart to the
// spawn_send LLM tool: today a user's only way to steer a running sub-agent
// is to pause the whole chat first, which this RPC exists to close.
//
// Delivery reuses the same drain machinery spawn_send does — the message is
// folded into the target thread's history at its next agent-loop step
// boundary, not synchronously.
func (s *ChatService) SendAgentMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.SendAgentMessageRequest],
) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("thread_id is required"))
	}
	if strings.TrimSpace(req.Msg.Message) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}

	// Ownership check: the chat must belong to the caller. Combined with the
	// thread.ChatID check below, this is what stops a user from addressing
	// an arbitrary thread in someone else's chat.
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil || chat == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	target, err := s.database.GetThread(ctx, req.Msg.ThreadId)
	if err != nil || target == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("thread not found"))
	}
	if target.ChatID != req.Msg.ChatId {
		// Deliberately the same NotFound the chat-ownership check above
		// returns, rather than a distinguishable error: revealing that a
		// thread ID exists but belongs to a different chat is an
		// enumeration leak we don't need to offer.
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("thread not found"))
	}

	owningWorkflow := s.owningWorkflow(ctx, target)
	isMainThread := req.Msg.ThreadId == chat.MainThreadID()

	// A message to the chat's MAIN thread after its run ended is a message to
	// the chat, and gets what SendMessage gives one: a run started to answer
	// it. The composer routes a send to this queue for as long as it shows
	// the run as working — including the instant the run finishes — so
	// refusing here left the message in the composer to be sent a second
	// time. Only a run that ended with no position to resume from qualifies
	// (completed, or stopped by the user): those are the runs SendMessage
	// starts afresh. A failed run is SendMessage's to resume, so it is still
	// refused below.
	restartMainRun := isMainThread && s.tempClient != nil && s.runs != nil && owningWorkflow != nil &&
		owningWorkflow.Status.IsStopped() && !owningWorkflow.Status.Resumable()

	// A terminal thread means the loop has exited. This is trustworthy in the
	// closing direction because a thread is only revived by the run that
	// starts on it (WorkflowStatusActivity's "started" arm calls
	// ReviveThread), so a terminal status here is never merely a stale stamp
	// left by an earlier turn of a reused main thread.
	//
	// Deliberately NOT self-healed here from "the workflow is running", even
	// though that is the shape the revival repairs: a spawn thread that has
	// just written its own "completed" sits in exactly that state for the
	// moment before its workflow follows, and reviving it would queue a
	// message into an agent that really has stopped.
	if !restartMainRun && core.ThreadStatusIsTerminal(target.Status) {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: fmt.Sprintf(
				"This agent has already finished (status: %s) — its loop has exited and there is nothing to deliver into. "+
					"Send a new message to the chat instead.",
				core.ThreadStatusLabel(target.Status)),
		}), nil
	}

	// A non-terminal thread status is NOT sufficient to prove there is a next
	// turn to deliver into, so the terminal check above cannot stand alone.
	//
	// Delivery only happens in CallLLM, which drains the thread's mailbox
	// before it reads history. An agent that is not executing never reaches
	// another CallLLM, so a message queued to one sits at status=queued
	// indefinitely while the receipt claims it will be read next turn.
	//
	// threads.status cannot answer this. It is written by the ThreadStatus
	// activity, which only ever records "started" (=running) and a terminal
	// verb — there is no "went idle" transition, so a thread that stopped
	// without its workflow completing keeps claiming running forever. That is
	// not a corner case: in the live DB every one of the 142 main threads is
	// status=running with zero exceptions and no completed_at, including
	// chats last active weeks ago. Trusting it is exactly the bug.
	//
	// The workflow that owns the thread is the signal that does move. It is
	// reconciled against Temporal (the actual execution), so it is the same
	// truth the send path and the reconciler already act on, and it is one
	// indexed primary-key read rather than a Temporal round trip on this hot
	// user-facing path.
	//
	// PENDING and PAUSED count as live — see WorkflowStatus.Live. A message
	// queued to either IS drained when the run starts or resumes, and
	// refusing it would lose a message that would have arrived.
	//
	// A workflow row that cannot be read fails open (owningWorkflow logs it):
	// it is not proof the agent is idle, and wrongly refusing loses the
	// message outright, whereas wrongly accepting only reproduces today's
	// late delivery.
	if !restartMainRun && owningWorkflow != nil && !owningWorkflow.Status.Live() {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: fmt.Sprintf(
				"This agent isn't currently running (its run is %s), so there is no next turn to deliver into. "+
					"Send a normal message to the chat to start one.",
				owningWorkflow.Status.Label()),
		}), nil
	}

	// The ended run left the thread terminal, and a terminal thread with
	// queued mail whose run Temporal reports closed is exactly what the
	// reconciler's orphaned-mailbox sweep marks undelivered. Revive it before
	// the row exists, so the sweep never sees the row in that shape; the run
	// that drains it — this one's successor, or the one started below — would
	// revive it anyway.
	if restartMainRun {
		if _, err := s.database.ReviveThread(ctx, req.Msg.ThreadId); err != nil {
			logging.Warn("Could not revive the main thread before queueing to its ended run",
				"error", err, "chatID", req.Msg.ChatId, "threadID", req.Msg.ThreadId)
		}
	}

	msg := &db.AgentMessage{
		ID: clientMessageID(req.Msg.GetClientMessageId()),
		// FromThreadID is a required FK to threads(id), and the human
		// sending this has no thread of their own. The chat's root thread
		// is the closest stable stand-in for "the user's side of this
		// conversation" — Kind (HumanMessage) is what actually tells the
		// drain envelope this came from the user rather than a peer agent,
		// so FromThreadID here is not read as a sender label for this kind.
		FromThreadID: chat.MainThreadID(),
		ChatID:       req.Msg.ChatId,
		ToThreadID:   req.Msg.ThreadId,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         req.Msg.Message,
		Attachments:  req.Msg.Attachments,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now(),
	}
	if err := s.database.EnqueueAgentMessage(ctx, msg); err != nil {
		logging.Error("Failed to queue agent message", "error", err, "chatID", req.Msg.ChatId, "threadID", req.Msg.ThreadId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to queue message"))
	}

	switch {
	case !isMainThread:
		_ = s.notifyThreadWake(ctx, chat, req.Msg.ThreadId, threadwake.ReasonMailbox)
	case restartMainRun && owningWorkflow.Status.StopReason == core.StopReasonCancelled:
		// A stopped run exits without reading its doorbell, even while
		// Temporal still has it open, so a wake would strand the row. Start
		// the run directly, as SendMessage does for a stopped run.
		return s.startRunForQueuedRow(ctx, chat, req.Msg.ThreadId, msg.ID)
	case s.wakeLiveRun(ctx, chat, req.Msg.ThreadId, threadwake.ReasonMailbox):
		// The chat's run has closed — before this RPC, or between the
		// liveness read above and this wake — so nothing will drain the row.
		// Start a run for it rather than leave it for the reconciler to mark
		// undelivered. A completed run that is still open is inside its
		// end-of-run check, which takes the wake and continues as a fresh
		// run for it (late_wake.go), so it lands here only once it is closed.
		return s.startRunForQueuedRow(ctx, chat, req.Msg.ThreadId, msg.ID)
	}

	// The receipt is deliberately as honest as spawn_send's: queued does not
	// mean read, and does not mean acted on.
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: "Queued for delivery. It will be read at that agent's next turn — it has not been read yet.",
	}), nil
}

// owningWorkflow is the workflow row that owns a thread: the thread's own
// workflow, or the one named after it. Nil when it cannot be read.
func (s *ChatService) owningWorkflow(ctx context.Context, thread *db.Thread) *db.Workflow {
	workflowID := thread.ID
	if thread.WorkflowID != nil && *thread.WorkflowID != "" {
		workflowID = *thread.WorkflowID
	}
	wf, err := s.database.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		logging.Warn("Could not read owning workflow for agent-message liveness check; allowing the queue",
			"error", err, "chatID", thread.ChatID, "threadID", thread.ID, "workflowID", workflowID)
		return nil
	}
	return wf
}

// notifyThreadWake rings the thread-wake doorbell on the workflow that owns
// the recipient thread, so a thread parked waiting on its background spawns
// wakes up and takes a turn rather than sleeping until a sub-agent finishes.
//
// Two callers, one per reason:
//
//   - SendAgentMessage, after queuing a row into agent_messages. Without this
//     the enqueue is silent and delivery depends on the recipient reaching a
//     loop-step boundary for some other reason.
//   - SendMessage, after saving a user message to a thread whose run is live.
//     A PARKED loop never takes another turn without it, and a loop on its
//     LAST turn has already read history and would end the run with the
//     message unread; the run reads the wake in both places (late_wake.go).
//
// When the recipient is a parent blocked in awaitLiveDetachedSpawns — the
// ordinary state of a main thread that has fanned work out to sub-agents — no
// boundary is scheduled either way, so "it will be read at that agent's next
// turn" was a promise with no turn behind it.
//
// threadID is load-bearing and must be the ACTUAL recipient. Every thread in a
// chat is driven by one Temporal execution, so the workflow this signals is
// always the chat's; the payload's thread is the only thing that selects which
// gate wakes. Passing the root thread for a message aimed at a sub-thread
// would wake the parent for input that is not its own.
//
// Never fails the RPC: whatever prompted the wake is already durable — the
// mailbox row, or the saved message — and failing would report failure for a
// message that IS saved. The error is returned for the callers that must act
// on it: a wake that reaches no run means the run closed after the caller
// found it live, and nothing will read the input until a run is started for
// it (wakeLiveRun).
func (s *ChatService) notifyThreadWake(ctx context.Context, chat *db.Chat, threadID string, reason threadwake.Reason) error {
	if s.tempClient == nil || threadID == "" {
		return nil
	}
	// Signal the chat's own workflow. A spawn is not a Temporal execution of
	// its own (dispatchSpawnBackground runs it as a goroutine inside the
	// parent), so every thread in the chat — main or spawned — is driven by
	// this one execution, and its tracker is where the notification must
	// land. Addressing the recipient thread inside the payload is what lets
	// the right gate wake.
	workflowID := chat.ID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}
	if err := s.tempClient.SignalWorkflow(ctx, workflowID, "", v2.ThreadWakeSignalName, v2.ThreadWakeSignal{
		Thread: threadID,
		Reason: reason,
	}); err != nil {
		logging.Warn("Could not wake thread",
			"error", err, "chatID", chat.ID, "threadID", threadID, "workflowID", workflowID, "reason", string(reason))
		return err
	}
	logging.Info("Woke thread",
		"chatID", chat.ID, "threadID", threadID, "workflowID", workflowID, "reason", string(reason))
	return nil
}

// startRunForQueuedRow starts a run for a row queued to the chat's main thread
// whose run had ended, or closed before the doorbell reached it. The new run's
// first call_llm drains the row.
//
// It is the run the closed one would have continued as had the doorbell
// arrived a moment sooner (late_wake.go): the same workflow at graph entry on
// the same thread, with the closed run's own inputs — read back with its
// get_workflow_inputs query, which Temporal answers for a closed workflow too.
// The queue carries no params of its own to build fresh inputs from.
//
// Ordering is what keeps the reconciler off the row. The closed run left the
// thread terminal, and a terminal thread with queued mail is exactly what
// resolveOrphanedAgentMessages marks undelivered; so the thread is revived
// BEFORE the run starts, and the sweep never sees the row as orphaned.
//
// If the inputs cannot be read, the row is withdrawn and the user told to send
// it as a message — the composer keeps the text on a refusal — rather than
// promised a delivery nothing will make.
func (s *ChatService) startRunForQueuedRow(ctx context.Context, chat *db.Chat, threadID, messageID string) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	workflowID := chat.MainThreadID()
	refuse := func(cause error) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
		logging.Warn("Could not start a run for a message queued to a run that closed; withdrawing it",
			"error", cause, "chatID", chat.ID, "workflowID", workflowID)
		if _, err := s.database.CancelQueuedAgentMessage(ctx, messageID, chat.ID); err != nil {
			logging.Warn("Could not withdraw the queued message", "error", err, "chatID", chat.ID, "messageID", messageID)
		}
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: "The agent finished just as this was queued, so there was no turn to deliver it into. Send it as a normal message instead.",
		}), nil
	}

	if chat.WorkflowName == nil || *chat.WorkflowName == "" {
		return refuse(fmt.Errorf("chat has no workflow"))
	}
	encoded, err := s.tempClient.QueryWorkflow(ctx, workflowID, "", "get_workflow_inputs")
	if err != nil {
		return refuse(err)
	}
	var inputs map[string]interface{}
	if err := encoded.Get(&inputs); err != nil {
		return refuse(err)
	}
	// This run answers a person, not whatever launched the chat.
	delete(inputs, v2.InputKeyLaunchRun)
	launch.InjectSessionDaemonID(inputs, chat)

	if _, err := s.database.ReviveThread(ctx, threadID); err != nil {
		return refuse(err)
	}

	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chat.ID,
		WorkflowName: *chat.WorkflowName,
		Thread:       threadID,
		ThreadMode:   model.ThreadModeNew,
	}
	if jwt, ok := auth.GetUserJWT(chat.UserID); ok {
		execContext.UserJWT = jwt
	}
	run, err := s.tempClient.ExecuteWorkflow(ctx, s.newRunOptions(workflowID), v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID:       chat.ID,
		WorkflowName: *chat.WorkflowName,
		Inputs:       inputs,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.database, chat.ID),
	})
	if err != nil {
		return refuse(err)
	}
	s.runs.RecordRun(ctx, chat.ID, workflowID, run.GetRunID())
	logging.Info("Started a run for a message queued as the previous one closed",
		"chatID", chat.ID, "workflowID", workflowID, "runID", run.GetRunID())
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: "The agent finished just as this was queued, so a new turn has started to read it.",
	}), nil
}

// newRunOptions is how every run SendMessage starts for an existing chat is
// started: under the chat's workflow ID, replacing any execution still holding
// it.
func (s *ChatService) newRunOptions(workflowID string) client.StartWorkflowOptions {
	return client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}
}

// wakeLiveRun rings the doorbell for input just saved to a run the caller
// found live, and reports whether that run has CLOSED instead — the one case
// where the caller must start a run for the input, because no turn of the old
// one will ever read it.
//
// A delivered signal needs nothing more: Temporal will not complete or
// continue-as-new the run without handing it the signal first, and the run
// gives a late wake a turn (late_wake.go). A failed one is only a closed run if
// Temporal says so. If the run is still open the failure was transient and the
// doorbell is rung once more; if Temporal cannot be asked, the answer is "not
// closed", because starting a second run over a live one would terminate it
// (WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING).
func (s *ChatService) wakeLiveRun(ctx context.Context, chat *db.Chat, threadID string, reason threadwake.Reason) (runClosed bool) {
	if s.notifyThreadWake(ctx, chat, threadID, reason) == nil || s.runs == nil {
		return false
	}
	workflowID := chat.ID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}
	state, err := s.runs.State(ctx, workflowID)
	if err != nil {
		logging.Warn("Could not tell whether a run that refused its wake is still open; leaving the input for its next turn",
			"error", err, "chatID", chat.ID, "workflowID", workflowID)
		return false
	}
	if !state.Exists || !state.IsRunning {
		return true
	}
	_ = s.notifyThreadWake(ctx, chat, threadID, reason)
	return false
}

// ListQueuedAgentMessages returns the entries currently sitting in a
// thread's mailbox (agent_messages) with status = queued -- what
// SendAgentMessage (or spawn_send) put there but the target thread hasn't
// drained yet. This is what makes a queued message visible to the user
// instead of it being invisible until the agent happens to drain it.
func (s *ChatService) ListQueuedAgentMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.ListQueuedAgentMessagesRequest],
) (*connect.Response[reliantv1.ListQueuedAgentMessagesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("thread_id is required"))
	}

	// Ownership check: mirrors SendAgentMessage exactly -- the chat must
	// belong to the caller, and the thread must belong to that chat, both
	// returning the same NotFound so thread IDs can't be enumerated.
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil || chat == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	target, err := s.database.GetThread(ctx, req.Msg.ThreadId)
	if err != nil || target == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("thread not found"))
	}
	if target.ChatID != req.Msg.ChatId {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("thread not found"))
	}

	queued, err := s.database.ListQueuedAgentMessagesForThread(ctx, req.Msg.ThreadId)
	if err != nil {
		logging.Error("Failed to list queued agent messages", "error", err, "chatID", req.Msg.ChatId, "threadID", req.Msg.ThreadId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list queued messages"))
	}

	messages := make([]*reliantv1.QueuedAgentMessage, len(queued))
	for i, msg := range queued {
		messages[i] = &reliantv1.QueuedAgentMessage{
			Id:          msg.ID,
			Body:        msg.Body,
			CreatedAt:   msg.CreatedAt.Format(time.RFC3339),
			SenderKind:  int32(msg.Kind),
			Attachments: msg.Attachments,
		}
	}

	return connect.NewResponse(&reliantv1.ListQueuedAgentMessagesResponse{
		Messages: messages,
	}), nil
}

// CancelQueuedAgentMessage revokes a single queued mailbox entry before the
// target agent drains it.
//
// This is a race against the agent's own next turn: CallLLM's drain may pick
// the row up between the user seeing it and clicking cancel. The
// deletion is conditioned on status = queued at the database level (see
// CancelQueuedAgentMessage in the postgres store), so the outcome is never
// ambiguous -- either this call wins the row and deletes it, or the drain
// already won it and this call reports failure without touching the row.
func (s *ChatService) CancelQueuedAgentMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelQueuedAgentMessageRequest],
) (*connect.Response[reliantv1.CancelQueuedAgentMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.MessageId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message_id is required"))
	}

	// Ownership check: the chat must belong to the caller. The DELETE
	// itself is additionally scoped to chat_id, so even if a caller guessed
	// a message ID from another chat, this ownership check is what stops
	// them from cancelling it.
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil || chat == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	cancelled, err := s.database.CancelQueuedAgentMessage(ctx, req.Msg.MessageId, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to cancel queued agent message", "error", err, "chatID", req.Msg.ChatId, "messageID", req.Msg.MessageId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to cancel message"))
	}

	if !cancelled {
		return connect.NewResponse(&reliantv1.CancelQueuedAgentMessageResponse{
			Success: false,
			Message: "Already delivered to the agent — too late to cancel.",
		}), nil
	}

	return connect.NewResponse(&reliantv1.CancelQueuedAgentMessageResponse{
		Success: true,
		Message: "Cancelled. The agent will never see this message.",
	}), nil
}
