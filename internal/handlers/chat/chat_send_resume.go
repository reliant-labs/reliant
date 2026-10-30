package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

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
func (s *Service) resumeFailedQuestionWorkflow(
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
		logging.ErrorContext(ctx, "Failed to build question resume response data", "error", err, "questionID", question.ID)
		return nil, false, ""
	}

	// Resolve the DB question (so it is no longer pending regardless of outcome)
	// and persist the messages so the resumed run reads them at its next LLM
	// boundary.
	if err := s.deps.Repo.ResolveQuestion(ctx, question.ID, &responseData); err != nil {
		logging.WarnContext(ctx, "Failed to resolve question during resume", "error", err, "questionID", question.ID)
	}
	if err := s.deps.Repo.EmitQuestionUpdate(ctx, question.ChatID, db.QuestionUpdate{
		QuestionID: question.ID,
		ChatID:     question.ChatID,
		WorkflowID: question.WorkflowID,
		StepID:     question.StepID,
		Status:     "resolved",
	}); err != nil {
		logging.WarnContext(ctx, "Failed to emit question update during resume", "error", err, "questionID", question.ID)
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
		logging.WarnContext(ctx, "Failed to save resume messages during question resume", "error", saveErr)
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
		logging.InfoContext(ctx, "Question-parked workflow not reset-resumable - coarse restart at position",
			"chatID", req.Msg.ChatId, "workflowID", workflowID, "questionID", question.ID, "error", err)
		return nil, false, presavedID
	}
	newRunID := outcome.RunID

	logging.InfoContext(ctx, "Question-parked workflow reset-and-resumed via answer (precise nested resume)",
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
func (s *Service) resurrectGhostWorkflow(
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
		TaskQueue:                workflow.SharedTaskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}

	// Step 2: Build workflow inputs (with presets and model defaults)
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

	initialData, err := s.launcher().BuildChatWorkflowInputs(ctx, userID, chat, workflowName, effectivePresets, req.Msg.WorkflowParams)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	// Validate workflow inputs before starting, and before anything of the
	// send is written — a refused send leaves nothing behind (see
	// liveRunStateUpdate).
	if validationErrors := s.launcher().ValidateChatWorkflowInputs(ctx, userID, chat, workflowName, initialData); len(validationErrors) > 0 {
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save messages"))
	}
	modelMoves := s.fallBackFromUnservableModels(ctx, userID, chat, workflowName, workflowID, targetThread, initialData,
		s.selectionStoredModelIsCurrent(ctx, req.Msg.ChatId, initialData))
	s.refreshChatProvider(ctx, userID, chat, workflowName, initialData)
	_, _ = s.recordSelection(ctx, userID, chat, workflowName, req.Msg.WorkflowParams, initialData, modelMoves, 0)

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
	s.chatPinner().Pin(ctx, chat)
	launch.InjectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.deps.Repo, req.Msg.ChatId),
		// A ghost (Temporal lost the running execution) is an infra failure,
		// not user intent — the fresh execution resumes at position.
		Resume: resumeWholeChatInput(v2.ResumeInputFromDurableState(ctx, s.deps.Repo, req.Msg.ChatId, workflowID)),
	}

	// Step 5: Start fresh Temporal execution
	workflowRun, err := s.deps.Temporal.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resurrect workflow"))
	}

	runID := workflowRun.GetRunID()
	logging.InfoContext(ctx, "[Ghost Recovery] Successfully resurrected workflow",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"newRunID", runID,
		"workflowName", workflowName,
		"thread", targetThread,
	)

	// Step 6: Update run IDs and workflow status
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)
	// Only over the status the row had before the start: the new run may
	// already have written its own (workflow.MarkWokenRunActive).
	workflow.MarkWokenRunActive(ctx, s.deps.Repo, workflowID, existingWorkflow.Status, true)

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
