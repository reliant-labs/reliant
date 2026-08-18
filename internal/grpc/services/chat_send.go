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
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"

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

// resumeInputForInterruptedRun builds the engine resume parameter for a new
// run whose predecessor was interrupted (failed/terminated/wedged/lost) rather
// than completed or user-cancelled. It reads the position checkpoint written
// at node-entry/loop-iteration boundaries. A missing checkpoint still returns
// a non-nil (empty) ResumeInput: resume mode stays on and the engine applies
// its fallbacks (workflow resume_node -> single top-level loop -> graph entry).
func (s *ChatService) resumeInputForInterruptedRun(ctx context.Context, workflowID string) *v2.ResumeInput {
	cp, err := s.database.GetWorkflowCheckpoint(ctx, workflowID)
	if err != nil {
		logging.Warn("Failed to load workflow checkpoint - resuming with engine fallbacks",
			"workflowID", workflowID, "error", err)
	}
	if cp == nil {
		return &v2.ResumeInput{}
	}
	return &v2.ResumeInput{
		NodeID:        cp.NodeID,
		LoopIteration: int(cp.LoopIteration),
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
func (s *ChatService) resumeFailedQuestionWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	chat *db.Chat,
	existingWorkflow *db.Workflow,
	question *db.Question,
	targetThread, userID, userContent string,
	systemMessages []*reliantv1.InputMessage,
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
	// The dead run is parked, not looping, so nothing has drained this thread's
	// mailbox — the answer is saved through the same absorb-first seam every
	// other non-running path uses, so anything queued keeps its place ahead of
	// it. Save failures stay non-fatal here (the question is already resolved
	// and blocking the resume helps nobody), and that is only safe BECAUSE the
	// seam is transactional: a failure rolls the mailbox claim back with the
	// saves, so the queued rows survive to be absorbed by the next send.
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

	// Update selected presets if provided
	if len(req.Msg.SelectedPresets) > 0 {
		chat.SelectedPresets = req.Msg.SelectedPresets
		chat.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateChat(ctx, chat); err != nil {
			logging.Error("[Ghost Recovery] Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
			// Non-fatal, continue
		}
	}

	// Determine target thread - default to root workflow thread
	targetThread := existingWorkflow.Thread
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// Step 1: Save messages BEFORE starting workflow. The DB said running but
	// Temporal lost the execution, so nothing is draining this thread's
	// mailbox — anything queued there is absorbed ahead of the new message.
	savedMessageID, err := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
	if err != nil {
		logging.Error("[Ghost Recovery] Failed to save messages", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Step 2: Build workflow options - same ID, fresh execution
	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// Step 4: Build workflow inputs (with presets and model defaults)
	// Use worktree path if available, otherwise project path
	projectPath := s.getEffectiveWorkingPath(ctx, chat)

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

	initialData := s.buildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Validate workflow inputs before starting
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Step 5: Build execution context - inheriting existing thread
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
	injectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		// A ghost (Temporal lost the running execution) is an infra failure,
		// not user intent — the fresh execution resumes at position.
		Resume: s.resumeInputForInterruptedRun(ctx, workflowID),
	}

	// Step 6: Start fresh Temporal execution
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

	// Step 7: Update run IDs and workflow status
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

	// Note: Previously checked if workflow completed and thread is closed.
	// Removed to allow restarting workflows - SendMessage will start a new workflow
	// for completed/failed/cancelled workflows (see status switch below).

	if err := validateWorkflowParamStructure(req.Msg.WorkflowParams); err != nil {
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

	// Check workflow status to decide: resume paused, send to running, or start new
	// This must happen atomically to avoid race conditions
	if workflowID := chat.MainThreadID(); workflowID != "" {
		// Use transaction to atomically check status and update
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
			case db.Paused():

				// Discuss mode: lightweight LLM chat without resuming the workflow
				if req.Msg.Discuss {
					return s.handleDiscussMode(ctx, req, chat, existingWorkflow, workflowID, userID, userContent, hasUserContent, systemMessages)
				}

				// Update selected presets on chat if provided (before starting workflow)
				if len(req.Msg.SelectedPresets) > 0 {
					chat.SelectedPresets = req.Msg.SelectedPresets
					chat.UpdatedAt = time.Now().UTC()
					if err := s.database.UpdateChat(ctx, chat); err != nil {
						logging.Error("Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
						// Don't fail - non-critical
					}
				}

				// Determine target thread - use workflow's thread from DB (not root workflow ID)
				// to handle forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Atomically: absorb the mailbox and save messages. A paused run
				// takes no loop steps, so nothing has drained the thread's
				// mailbox — saveIncomingMessages claims it first so queued text
				// keeps its place ahead of this new message. RunTx is
				// re-entrant, so its transaction joins this one and the claim
				// commits with the saves.
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
				err := s.database.RunTx(ctx, func(txCtx context.Context) error {
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

				// Signal workflow with param/preset input updates when provided.
				if len(req.Msg.WorkflowParams) > 0 || len(req.Msg.SelectedPresets) > 0 {
					stateUpdate := s.buildStateUpdateForActiveWorkflow(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.SelectedPresets, req.Msg.WorkflowParams)

					// Validate model selectors in updated params
					if validationErrors := s.validateWorkflowInputs(ctx, existingWorkflow.WorkflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
						errMsgs := make([]string, len(validationErrors))
						for i, e := range validationErrors {
							errMsgs[i] = e.Error()
						}
						return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
					}

					// Only add "params changed" message if params actually changed from current workflow state
					if len(stateUpdate) > 0 && s.checkParamsActuallyChanged(ctx, workflowID, runID, stateUpdate) {
						hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
						_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
						if err != nil {
							logging.Warn("Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
						}
					}

					if len(stateUpdate) > 0 {
						if err := s.tempClient.SignalWorkflow(ctx, workflowID, runID, "update_workflow_state", stateUpdate); err != nil {
							logging.Warn("Failed to signal workflow with param updates", "error", err, "workflowID", workflowID)
						}
					}
				}

				// Resume the run. The service signals a live execution,
				// reset-replays a dead-but-replayable one, and refreshes the
				// chat's run id when a reset minted a new one.
				outcome, resumeErr := s.runs.Resume(ctx, req.Msg.ChatId)
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
					resumeInput = s.resumeInputForInterruptedRun(ctx, workflowID)
					resumeThread = existingWorkflow.Thread
					// Fall through to start a new workflow below
					break
				}

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

				// Update selected presets on chat if provided
				if len(req.Msg.SelectedPresets) > 0 {
					chat.SelectedPresets = req.Msg.SelectedPresets
					chat.UpdatedAt = time.Now().UTC()
					if err := s.database.UpdateChat(ctx, chat); err != nil {
						logging.Error("Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
						// Don't fail - non-critical
					}
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

				// Save the user message
				var savedMsg *db.Message
				if hasUserContent || len(req.Msg.Attachments) > 0 {
					var err error
					savedMsg, err = s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
					if err != nil {
						logging.Error("Failed to save message to running workflow", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save message: %w", err))
					}
				}

				// Signal workflow with param/preset input updates when provided.
				if len(req.Msg.WorkflowParams) > 0 || len(req.Msg.SelectedPresets) > 0 {
					stateUpdate := s.buildStateUpdateForActiveWorkflow(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.SelectedPresets, req.Msg.WorkflowParams)

					// Validate model selectors in updated params
					if validationErrors := s.validateWorkflowInputs(ctx, existingWorkflow.WorkflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
						errMsgs := make([]string, len(validationErrors))
						for i, e := range validationErrors {
							errMsgs[i] = e.Error()
						}
						return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
					}

					runID := ""
					if chat.RunID != nil {
						runID = *chat.RunID
					}

					// Only add "params changed" message if params actually changed from current workflow state
					if len(stateUpdate) > 0 && s.checkParamsActuallyChanged(ctx, workflowID, runID, stateUpdate) {
						hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
						_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
						if err != nil {
							logging.Error("Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
							// Non-fatal, continue to signal
						}
					}

					if len(stateUpdate) > 0 {
						if err := s.tempClient.SignalWorkflow(ctx, workflowID, runID, "update_workflow_state", stateUpdate); err != nil {
							logging.Warn("Failed to signal workflow with param updates", "error", err, "workflowID", workflowID)
						}
					}
				}

				// Get RunID from chat
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				workflowStatus := fmt.Sprintf("%d", db.Active())
				var messageID string
				if savedMsg != nil {
					messageID = savedMsg.ID
				}
				go s.trackMessageSent(ctx, userID, chat, messageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      messageID,
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
					resp, resumed, presavedID := s.resumeFailedQuestionWorkflow(ctx, req, chat, existingWorkflow, pendingQuestion, targetThread, userID, userContent, systemMessages)
					if resumed {
						return resp, nil
					}
					// Guard-exhausted / not reset-resumable: the question is already
					// resolved and messages saved — fall through to coarse restart.
					resumeMessagesSaved = true
					resumePresavedMessageID = presavedID
					resumeInput = s.resumeInputForInterruptedRun(ctx, workflowID)
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

					outcome, resumeErr := s.runs.ResumeInterrupted(ctx, req.Msg.ChatId)
					if resumeErr != nil {
						return nil, connect.NewError(connect.CodeInternal, resumeErr)
					}
					if outcome.Kind == runs.OutcomeResumed {
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
				resumeInput = s.resumeInputForInterruptedRun(ctx, workflowID)
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

	needsChatUpdate := false

	// Use existing workflow ID from chat, or use chat ID as root workflow ID
	var workflowID string
	if id := chat.MainThreadID(); id != "" {
		workflowID = id
	} else {
		workflowID = req.Msg.ChatId // Root workflow ID = chat ID
		chat.WorkflowID = &workflowID
		needsChatUpdate = true
	}

	// Handle workflow switching - only allowed when root workflow is pending (chat hasn't started)
	if req.Msg.Workflow != nil && *req.Msg.Workflow != "" && *req.Msg.Workflow != workflowName {
		// Check if root workflow is pending
		rootWorkflow, err := s.database.GetWorkflow(ctx, workflowID)
		if err != nil {
			logging.Error("Failed to get root workflow", "error", err, "workflowID", workflowID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check workflow status"))
		}

		// Allow switching if workflow doesn't exist yet (new chat) or is pending (branched chat)
		if rootWorkflow != nil && rootWorkflow.Status != db.Pending() {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot change workflow after chat has started - use Branch to create a new chat with a different workflow"))
		}

		// Validate workflow exists
		if _, err := s.loadWorkflowForValidation(ctx, *req.Msg.Workflow, chat.ProjectID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", *req.Msg.Workflow))
		}

		// Update workflow name on both chat and workflow record
		workflowName = *req.Msg.Workflow
		chat.WorkflowName = req.Msg.Workflow
		needsChatUpdate = true

		if rootWorkflow != nil {
			if err := s.database.UpdateWorkflowName(ctx, workflowID, workflowName); err != nil {
				logging.Error("Failed to update workflow name", "error", err, "workflowID", workflowID)
				// Continue - chat update is more important
			}
		}
	}

	// Update selected presets if provided
	if len(req.Msg.SelectedPresets) > 0 {
		chat.SelectedPresets = req.Msg.SelectedPresets
		needsChatUpdate = true
	}

	// Update chat if needed
	if needsChatUpdate {
		chat.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateChat(ctx, chat); err != nil {
			logging.Error("Failed to update chat", "error", err, "chatID", req.Msg.ChatId)
			// Don't fail the request for non-critical updates
		}
	}

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

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
	projectPath := s.getEffectiveWorkingPath(ctx, chat)

	// Build workflow inputs from merged presets and user params
	initialData := s.buildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Determine target thread. Resume runs continue the interrupted run's
	// thread (which may be a forked/child thread) so history stays continuous.
	targetThread := workflowID
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

	// Save messages BEFORE starting workflow for consistency: any mailbox the
	// user queued into this thread, then system messages, then the new user
	// message. Nothing has been draining this thread (the prior run completed,
	// was cancelled, or died), so absorbing here is what keeps the queued text
	// ahead of the message that follows it instead of behind it.
	//
	// A resume branch (reset-and-replay fallback) may have already persisted the
	// messages before its reset attempt — skip re-saving so they aren't doubled.
	var savedMessageID string
	if resumeMessagesSaved {
		savedMessageID = resumePresavedMessageID
	} else {
		// A chat opening on a directory with no code is a greenfield request,
		// and the stack is still undecided. Hand the model that observation
		// plus the criteria for proposing forge, ahead of the user's first
		// message so it is in view when the model reads the ask. No-ops on
		// every later turn and whenever the project already holds code.
		if guidance := s.maybeGreenfieldGuidance(ctx, userID, chat); guidance != nil {
			systemMessages = append([]*reliantv1.InputMessage{guidance}, systemMessages...)
		}

		saved, err := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
		if err != nil {
			logging.Error("Failed to save messages for new workflow", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		savedMessageID = saved
	}

	// Validate workflow inputs before starting
	// This catches missing required inputs early (400) instead of at runtime
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Inject session daemon if set on chat
	injectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Resume:       resumeInput,
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
	}), nil
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
	if core.ThreadStatusIsTerminal(target.Status) {
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
	owningWorkflowID := target.ID
	if target.WorkflowID != nil && *target.WorkflowID != "" {
		owningWorkflowID = *target.WorkflowID
	}
	owningWorkflow, err := s.database.GetWorkflow(ctx, owningWorkflowID)
	if err != nil || owningWorkflow == nil {
		// Fail open. A thread whose workflow row we cannot read is not proof
		// the agent is idle, and wrongly refusing loses the message outright,
		// whereas wrongly accepting only reproduces today's late delivery.
		logging.Warn("Could not read owning workflow for agent-message liveness check; allowing the queue",
			"error", err, "chatID", req.Msg.ChatId, "threadID", req.Msg.ThreadId, "workflowID", owningWorkflowID)
	} else if !owningWorkflow.Status.Live() {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: fmt.Sprintf(
				"This agent isn't currently running (its run is %s), so there is no next turn to deliver into. "+
					"Send a normal message to the chat to start one.",
				owningWorkflow.Status.Label()),
		}), nil
	}

	msg := &db.AgentMessage{
		ID: uuid.New().String(),
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

	s.notifyAgentMessageQueued(ctx, chat, req.Msg.ThreadId)

	// The receipt is deliberately as honest as spawn_send's: queued does not
	// mean read, and does not mean acted on.
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: "Queued for delivery. It will be read at that agent's next turn — it has not been read yet.",
	}), nil
}

// notifyAgentMessageQueued rings the mailbox doorbell on the workflow that
// owns the recipient thread, so a thread parked waiting on its background
// spawns wakes up and drains rather than sleeping until a sub-agent finishes.
//
// Without this the enqueue above is silent, and delivery depends entirely on
// the recipient reaching a loop-step boundary for some other reason. When the
// recipient is a parent blocked in awaitLiveDetachedSpawns — the ordinary
// state of a main thread that has fanned work out to sub-agents — no such
// boundary is scheduled, so "it will be read at that agent's next turn" was a
// promise with no turn behind it.
//
// Deliberately best-effort. The row is already durably queued, and the drain
// reads it from the database, so a signal that cannot be delivered costs a
// late delivery (the pre-existing behavior), not a lost message. Failing the
// RPC here would be strictly worse: it would report failure for a message
// that IS queued and WILL be read at the next boundary.
func (s *ChatService) notifyAgentMessageQueued(ctx context.Context, chat *db.Chat, threadID string) {
	if s.tempClient == nil {
		return
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
	if err := s.tempClient.SignalWorkflow(ctx, workflowID, "", v2.AgentMessageQueuedSignalName, v2.AgentMessageQueuedSignal{
		Thread: threadID,
	}); err != nil {
		logging.Warn("Could not notify workflow of queued agent message; it will be delivered at the next loop boundary",
			"error", err, "chatID", chat.ID, "threadID", threadID, "workflowID", workflowID)
		return
	}
	logging.Info("Notified workflow of queued agent message",
		"chatID", chat.ID, "threadID", threadID, "workflowID", workflowID)
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
