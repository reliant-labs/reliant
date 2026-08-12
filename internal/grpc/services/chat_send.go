// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
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

// absorbQueuedMailbox takes every still-queued HUMAN message addressed to a
// thread and writes each one into that thread's transcript as an ordinary user
// message, oldest first.
//
// This exists because messages.seq — the chat-global counter the transcript is
// ordered by — is allocated at SAVE time, not at send time. A message the user
// queued into a busy agent is only drained at the agent's next loop-step
// boundary, so if a new send starts the run that eventually drains it, the
// OLDER queued text is saved AFTER the newer message and renders beneath it.
// That is the "my most recent message shows first" bug. Absorbing the mailbox
// immediately before the new message is saved restores send order.
//
// MUST be called inside the caller's transaction, alongside the save of the
// new user message. ClaimQueuedAgentMessagesForThread is a DELETE ... RETURNING:
// if the claim commits and the saves do not, the rows are gone and the user's
// words exist nowhere. Every error here is therefore returned, never logged and
// swallowed — an aborted transaction rolls the DELETE back and leaves the
// messages queued for the next attempt, which is strictly recoverable. A
// best-effort absorb would trade a visible ordering bug for silent data loss.
//
// Peer-agent rows (spawn_send, kind 1-4) are deliberately untouched: the claim
// query is scoped to kind = 5, so a sub-agent's report still reaches the
// orchestrator through the normal drain with its envelope framing intact.
func (s *ChatService) absorbQueuedMailbox(ctx context.Context, chatID, thread, workflowID string) (int, error) {
	claimed, err := s.database.ClaimQueuedAgentMessagesForThread(ctx, thread, chatID, "")
	if err != nil {
		return 0, fmt.Errorf("failed to claim queued messages for thread %s: %w", thread, err)
	}
	if len(claimed) == 0 {
		return 0, nil
	}

	// Oldest first — the claim is sorted by created_at, and each save takes the
	// next seq, so the transcript ends up in the order the user typed.
	// Attachments carry through exactly as SendAgentMessage stored them, so a
	// queued screenshot reaches the LLM as if it had been sent directly.
	for _, m := range claimed {
		if _, err := s.database.SaveMessageToThread(ctx, chatID, thread,
			int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), m.Body, &workflowID, m.Attachments, nil); err != nil {
			return 0, fmt.Errorf("failed to persist claimed message %s: %w", m.ID, err)
		}
	}

	logging.Info("Absorbed queued mailbox messages into the transcript ahead of a new send",
		"chatID", chatID, "thread", thread, "count", len(claimed))
	return len(claimed), nil
}

// saveIncomingMessages persists everything a send contributes to a thread's
// transcript, atomically and in the order the user experienced it: first any
// messages they had QUEUED into this thread's mailbox while the agent was
// busy, then this send's system messages, then the new user message. The saved
// user-message ID is returned (empty when there is no user content).
//
// One transaction covers the mailbox claim and every save — see
// absorbQueuedMailbox for why splitting them would destroy messages.
//
// Called ONLY on paths where the workflow is not running. A live agent drains
// its own mailbox correctly at its next loop-step boundary; claiming those rows
// out from under it would both reorder them and defeat the mailbox's purpose.
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
		if _, err := s.absorbQueuedMailbox(txCtx, req.Msg.ChatId, thread, workflowID); err != nil {
			return err
		}

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

	// Deliver the answer. SignalWithRecovery reset-replays the dead run (honoring
	// the reset guard) and re-sends the signal on the new run.
	signalData := map[string]interface{}{
		"status":        "resolved",
		"response_data": responseData,
	}
	if err := s.pauseService.SignalWithRecovery(ctx, question.TemporalWorkflowID, "signal.question."+question.ID, signalData); err != nil {
		logging.Info("Question-parked workflow not reset-resumable - coarse restart at position",
			"chatID", req.Msg.ChatId, "workflowID", workflowID, "questionID", question.ID, "error", err)
		return nil, false, presavedID
	}

	if err := s.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning); err != nil {
		logging.Warn("Failed to mark workflow running after question resume", "error", err, "workflowID", workflowID)
	}
	newRunID := ""
	if st, e := s.getTemporalWorkflowState(ctx, workflowID); e == nil && st.Exists {
		newRunID = st.RunID
		s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, st.RunID)
	}

	logging.Info("Question-parked workflow reset-and-resumed via answer (precise nested resume)",
		"chatID", req.Msg.ChatId, "workflowID", workflowID, "questionID", question.ID, "newRunID", newRunID)

	workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
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
	s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, runID)
	if err := s.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning); err != nil {
		logging.Warn("[Ghost Recovery] Failed to update workflow status", "error", err, "workflowID", workflowID)
		// Non-fatal - workflow is running in Temporal
	}

	workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
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
			if existingWorkflow.Status == db.WorkflowStatusRunning {
				temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
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
					if existingWorkflow.Status == db.WorkflowStatusPaused {
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
						s.reconcileWorkflowStatus(ctx, workflowID, existingWorkflow.Status, temporalState.Status)
						existingWorkflow.Status = temporalState.Status
					}
				}
			}

			switch existingWorkflow.Status {
			case db.WorkflowStatusPaused:

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

				// Atomically: absorb the mailbox, save messages, mark running.
				// A paused run takes no loop steps, so nothing has drained the
				// thread's mailbox — saveIncomingMessages claims it first so
				// queued text keeps its place ahead of this new message. RunTx
				// is re-entrant, so its transaction joins this one and the
				// claim commits with the saves.
				var savedMessageID string
				err := s.database.RunTx(ctx, func(txCtx context.Context) error {
					var saveErr error
					savedMessageID, saveErr = s.saveIncomingMessages(txCtx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
					if saveErr != nil {
						return saveErr
					}

					// Update workflow status to running
					if err := s.database.UpdateWorkflowStatus(txCtx, workflowID, db.WorkflowStatusRunning); err != nil {
						return fmt.Errorf("failed to update workflow status: %w", err)
					}

					return nil
				})
				if err != nil {
					logging.Error("Failed to resume paused workflow", "error", err, "workflowID", workflowID)
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

				// Resume the workflow via PauseService which handles all cases:
				// - Live Temporal execution: sends signal.resume
				// - Expired/timed-out execution: resets to pause point and resumes
				// - Lost execution: returns ErrWorkflowNotFound
				if err := s.pauseService.ResumeWorkflow(ctx, workflowID, req.Msg.ChatId); err != nil {
					if errors.Is(err, workflow.ErrWorkflowNotFound) {
						logging.Warn("Paused workflow not found in Temporal during SendMessage - resuming at position with a new run",
							"workflowID", workflowID, "chatID", req.Msg.ChatId)
						// The paused run was lost (infra failure, not user
						// intent) — start a new run that resumes at position.
						resumeInput = s.resumeInputForInterruptedRun(ctx, workflowID)
						resumeThread = existingWorkflow.Thread
						// Fall through to start a new workflow below
						break
					}
					logging.Error("Failed to resume paused workflow",
						"error", err, "workflowID", workflowID, "chatID", req.Msg.ChatId)
				}

				// If reset-based resume happened, update the chat's run ID
				if newState, stateErr := s.getTemporalWorkflowState(ctx, workflowID); stateErr == nil && newState.Exists {
					if chat.RunID == nil || *chat.RunID != newState.RunID {
						s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, newState.RunID)
						runID = newState.RunID
					}
				}

				// Return with updated workflow_status so frontend knows we resumed
				workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
				go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      savedMessageID,
				}), nil

			case db.WorkflowStatusRunning:

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

				workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
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

			case db.WorkflowStatusExpired:
				// Expired workflow — reset to pause point and resume
				logging.Info("Expired workflow detected, attempting reset-based resume",
					"workflowID", workflowID,
					"chatID", req.Msg.ChatId,
				)

				// Use workflow's thread from DB (not root workflow ID) to handle
				// forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save messages before reset — mailbox first, so anything the
				// user queued while the run was alive keeps its place ahead of
				// this message rather than being drained after it.
				savedMessageID, err := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
				if err != nil {
					logging.Error("Failed to save messages for expired workflow resume", "error", err, "workflowID", workflowID)
					return nil, connect.NewError(connect.CodeInternal, err)
				}

				// Reset expired workflow (handles orphan repair, reset, resume signal)
				newRunID, err := s.pauseService.ResumeExpiredWorkflow(ctx, workflowID, req.Msg.ChatId)
				if err != nil {
					logging.Error("Failed to reset expired workflow", "error", err, "workflowID", workflowID)
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume expired workflow: %w", err))
				}

				// Update chat's run ID with the new run
				s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, newRunID)

				workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          newRunID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      savedMessageID,
				}), nil

			case db.WorkflowStatusFailed:
				// Check if this is a stuck workflow (DB says failed but Temporal says running)
				// Stuck workflows cannot be restarted - user must branch to continue
				temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
				if temporalErr == nil && temporalState.Exists && temporalState.IsRunning {
					logging.Warn("Attempted to resume stuck workflow - blocking",
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
					)
					return nil, connect.NewError(connect.CodeFailedPrecondition,
						fmt.Errorf("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message"))
				}

				// A run that died parked on an unanswered ask_question wakes on
				// signal.question.<id>, not signal.resume. Deliver the user's plain
				// message as the (marked) question answer via reset-and-replay so it
				// resumes PRECISELY (the rebuilt run re-parks on the same question
				// channel and receives it) instead of coarse-restarting the loop.
				var pendingQuestion *db.Question
				if temporalErr == nil && temporalState.Exists && !temporalState.IsRunning {
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
				if temporalErr == nil && temporalState.Exists && !temporalState.IsRunning && pendingQuestion == nil {
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

					newRunID, resumeErr := s.pauseService.ResumeInterruptedWorkflow(ctx, workflowID, req.Msg.ChatId)
					if resumeErr == nil {
						s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, newRunID)
						logging.Info("Interrupted workflow reset-and-resumed (precise nested resume)",
							"chatID", req.Msg.ChatId,
							"workflowID", workflowID,
							"newRunID", newRunID,
						)
						workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
						go s.trackMessageSent(ctx, userID, chat, presavedID, targetThread, userContent, len(req.Msg.Attachments))
						return connect.NewResponse(&reliantv1.SendMessageResponse{
							ChatId:         req.Msg.ChatId,
							WorkflowId:     workflowID,
							RunId:          newRunID,
							Status:         "processing",
							WorkflowStatus: &workflowStatus,
							MessageId:      presavedID,
						}), nil
					}
					if errors.Is(resumeErr, workflow.ErrHistoryLimitExceeded) {
						// The run exhausted Temporal's per-execution history
						// limit. Resetting cannot help (it forks from inside
						// the oversized history), so this goes to the coarse
						// fresh restart — and the user is told, because
						// otherwise the chat simply stops with no explanation
						// and "send a message" appears to do nothing.
						logging.Warn("Workflow hit Temporal's history limit - fresh restart at position",
							"chatID", req.Msg.ChatId,
							"workflowID", workflowID,
						)
						s.notifyHistoryLimitRestart(ctx, req.Msg.ChatId, workflowID, targetThread)
					} else if errors.Is(resumeErr, workflow.ErrNoReplayableHistory) || errors.Is(resumeErr, workflow.ErrResetAttemptsExhausted) {
						logging.Info("Interrupted workflow not reset-resumable - coarse restart at position",
							"chatID", req.Msg.ChatId,
							"workflowID", workflowID,
							"reason", resumeErr,
						)
					} else {
						logging.Error("Reset-and-replay of interrupted workflow failed - coarse restart at position",
							"chatID", req.Msg.ChatId,
							"workflowID", workflowID,
							"error", resumeErr,
						)
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

			case db.WorkflowStatusCancelled, db.WorkflowStatusCompleted:
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
		if rootWorkflow != nil && rootWorkflow.Status != db.WorkflowStatusPending {
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
	s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, runID)

	workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
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
	// Delivery only happens in drainAgentMessagesAtBoundary, which runs at an
	// agent loop-step boundary. An agent that is not executing takes no steps
	// and never drains, so a message queued to one sits at status=queued
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
	// PENDING and PAUSED count as live — see WorkflowStatusIsLive. A message
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
	} else if !core.WorkflowStatusIsLive(owningWorkflow.Status) {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: fmt.Sprintf(
				"This agent isn't currently running (its run is %s), so there is no next turn to deliver into. "+
					"Send a normal message to the chat to start one.",
				core.WorkflowStatusLabel(owningWorkflow.Status)),
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

	// The receipt is deliberately as honest as spawn_send's: queued does not
	// mean read, and does not mean acted on.
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: "Queued for delivery. It will be read at that agent's next turn — it has not been read yet.",
	}), nil
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
// This is a race against the agent's own loop boundary: drainAgentMessagesAtBoundary
// may pick the row up between the user seeing it and clicking cancel. The
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

// ClaimQueuedAgentMessages takes queued messages back off a thread's mailbox
// and hands them to the caller to resend as ordinary turns. It backs both
// "send now" (one entry) and "send all" (the whole queue).
//
// The UI used to do this as cancel-then-send from the client: cancel, check
// whether the cancel took, and only then send. That has a real gap between
// the two calls, and a bulk version multiplies it by the size of the queue —
// with partial failure leaving some messages sent and some delivered as
// queued, in an order nobody chose. Here the take is one DELETE ... RETURNING,
// so the rows that come back are exactly the rows this caller now owns; a row
// the drain won first never appears. There is no window to lose.
//
// The caller must resend precisely what was returned. Anything else — a stale
// local list, an optimistic assumption that the queue it rendered is still
// the queue — is what would let a message be both delivered and resent.
func (s *ChatService) ClaimQueuedAgentMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.ClaimQueuedAgentMessagesRequest],
) (*connect.Response[reliantv1.ClaimQueuedAgentMessagesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("thread_id is required"))
	}

	// Ownership: same two checks, same indistinguishable NotFound, as
	// SendAgentMessage and ListQueuedAgentMessages.
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

	claimed, err := s.database.ClaimQueuedAgentMessagesForThread(
		ctx, req.Msg.ThreadId, req.Msg.ChatId, req.Msg.GetMessageId())
	if err != nil {
		logging.Error("Failed to claim queued agent messages",
			"error", err, "chatID", req.Msg.ChatId, "threadID", req.Msg.ThreadId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to claim queued messages"))
	}

	messages := make([]*reliantv1.QueuedAgentMessage, len(claimed))
	for i, msg := range claimed {
		messages[i] = &reliantv1.QueuedAgentMessage{
			Id:          msg.ID,
			Body:        msg.Body,
			CreatedAt:   msg.CreatedAt.Format(time.RFC3339),
			SenderKind:  int32(msg.Kind),
			Attachments: msg.Attachments,
		}
	}

	return connect.NewResponse(&reliantv1.ClaimQueuedAgentMessagesResponse{
		Messages: messages,
	}), nil
}
