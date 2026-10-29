// Copyright (c) 2025 Reliant Labs
package question

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ResolveQuestion resolves a pending question with the user's answer
func (s *Service) ResolveQuestion(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveQuestionRequest],
) (*connect.Response[reliantv1.ResolveQuestionResponse], error) {
	if req.Msg.QuestionId == "" {
		return nil, svcerr.InvalidArgument("question_id is required")
	}
	if req.Msg.Action == "" {
		return nil, svcerr.InvalidArgument("action is required")
	}

	// Look up the question
	question, err := s.deps.Repo.GetQuestionByID(ctx, req.Msg.QuestionId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get question"))
	}
	if question == nil {
		return nil, svcerr.NotFound("question")
	}

	// Ownership is transitive through the chat: resolving a question signals
	// the chat's workflow, so a question id alone is not an authorization. A
	// foreign question is NotFound, never PermissionDenied — no cross-tenant
	// existence oracle.
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	chat, err := s.deps.Repo.GetChat(ctx, question.ChatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get question"))
	}
	if chat == nil || chat.UserID != userID {
		return nil, svcerr.NotFound("question")
	}

	if question.Status != db.QuestionStatusPending {
		return nil, svcerr.FailedPrecondition("question already resolved")
	}

	// Nothing waits on an async question: its answer is a message to the
	// thread that asked.
	if question.Async {
		return s.resolveAsyncQuestion(ctx, chat, question, req.Msg.GetResponseData())
	}

	// Resolve the question in the database
	var responseData *string
	if req.Msg.ResponseData != nil && *req.Msg.ResponseData != "" {
		responseData = req.Msg.ResponseData
	}

	if err := s.deps.Repo.ResolveQuestion(ctx, question.ID, responseData); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve question"))
	}

	// Emit question update (resolved)
	if err := s.deps.Repo.EmitQuestionUpdate(ctx, question.ChatID, db.QuestionUpdate{
		QuestionID: question.ID,
		ChatID:     question.ChatID,
		WorkflowID: question.WorkflowID,
		StepID:     question.StepID,
		Status:     "resolved",
	}); err != nil {
		logging.WarnContext(ctx, "[Question] Failed to emit question update", "error", err)
	}

	// For "reply" action with response_data and ask_user metadata: save user message to thread
	if req.Msg.Action == "reply" && responseData != nil && question.Metadata != nil {
		if isAskUserQuestion(question.Metadata) {
			if err := s.saveUserReplyMessage(ctx, question, *responseData); err != nil {
				logging.WarnContext(ctx, "[Question] Failed to save user reply message", "error", err, "questionID", question.ID)
			}
		}
	}

	// Signal the Temporal workflow
	s.signalQuestion(ctx, question, map[string]interface{}{
		"status":        "resolved",
		"response_data": stringPtrToString(responseData),
	})

	return connect.NewResponse(&reliantv1.ResolveQuestionResponse{
		Success: true,
	}), nil
}

// ListPendingQuestions returns every pending question in a chat, oldest first,
// or, with chat_id unset, in every chat the caller owns (set is a filter,
// unset is none; an empty chat_id is rejected rather than read as unset),
// each carrying the thread that asked it. A question whose asking run
// completed or was cancelled is left out: nothing can answer it, and shown in
// its thread's composer it would replace the input the user needs to message
// that thread.
//
// All of them, not "the chat's question": each spawned agent is a thread that
// can park on its own ask_user, so one chat can be waiting on the user in
// several places at once. Returning only the newest hid every older thread's
// question, and the user's reply to that thread went to its mailbox — which a
// thread parked on a question never drains.
//
// A set chat is verified to exist and be the caller's BEFORE the question lookup,
// which is the difference between "there is no gate" and "that is not a chat".
// Without the check both collapse into an empty-but-successful response, and
// the CLI renders it as "No open questions." with exit 0 — a clean-looking
// answer that is simply false. Someone supervising a run reads that and walks
// away from a workflow that is parked on a question. Every other supervision
// RPC (GetWorkflowExecutions, GetChatUpdates) already verifies.
func (s *Service) ListPendingQuestions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPendingQuestionsRequest],
) (*connect.Response[reliantv1.ListPendingQuestionsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var (
		questions []*db.Question
		err       error
	)
	if req.Msg.ChatId == nil {
		questions, err = s.deps.Repo.ListAnswerablePendingQuestionsByUser(ctx, userID)
	} else {
		chatID := req.Msg.GetChatId()
		if chatID == "" {
			return nil, svcerr.InvalidArgument("chat_id, when set, must not be empty")
		}
		chat, getErr := s.deps.Repo.GetChatRow(ctx, chatID)
		if getErr != nil {
			return nil, svcerr.WrapOr(getErr, svcerr.Internal("failed to get chat"))
		}
		if chat == nil || chat.UserID != userID {
			return nil, svcerr.NotFound("chat")
		}
		questions, err = s.deps.Repo.ListAnswerablePendingQuestionsByChat(ctx, chatID)
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list pending questions"))
	}

	resp := &reliantv1.ListPendingQuestionsResponse{
		Questions: make([]*reliantv1.QuestionInfo, 0, len(questions)),
	}
	for _, q := range questions {
		resp.Questions = append(resp.Questions, questionToProto(q))
	}
	return connect.NewResponse(resp), nil
}

// ============================================================================
// HELPERS
// ============================================================================

// questionToProto converts a db.Question to proto QuestionInfo
func questionToProto(q *db.Question) *reliantv1.QuestionInfo {
	info := &reliantv1.QuestionInfo{
		QuestionId: q.ID,
		ChatId:     q.ChatID,
		WorkflowId: q.WorkflowID,
		ThreadId:   q.ThreadID,
		StepId:     q.StepID,
		Status:     questionStatusString(q.Status),
		CreatedAt:  q.CreatedAt.Format(time.RFC3339),
	}
	if q.Metadata != nil {
		info.Metadata = q.Metadata
	}
	info.Async = q.Async
	if q.ToolCallID != nil {
		info.ToolCallId = *q.ToolCallID
	}
	if q.ResponseData != nil {
		info.ResponseData = q.ResponseData
	}
	if q.WithdrawnReason != nil {
		info.WithdrawnReason = q.WithdrawnReason
	}
	return info
}

// questionStatusString converts an int status to string
func questionStatusString(status int) string {
	switch status {
	case db.QuestionStatusPending:
		return "pending"
	case db.QuestionStatusResolved:
		return "resolved"
	default:
		return "unknown"
	}
}

// signalQuestion sends a signal to the workflow using PauseService.SignalWithRecovery
func (s *Service) signalQuestion(ctx context.Context, question *db.Question, signalData map[string]interface{}) {
	if question.TemporalWorkflowID == "" {
		logging.WarnContext(ctx, "[Question] No temporal_workflow_id on question, cannot signal", "questionID", question.ID)
		return
	}
	signalName := "signal.question." + question.ID
	if err := s.deps.Signals.SignalWithRecovery(ctx, question.TemporalWorkflowID, signalName, signalData, nil); err != nil {
		logging.WarnContext(ctx, "[Question] Failed to signal question resolution",
			"error", err,
			"questionID", question.ID,
			"temporalWorkflowID", question.TemporalWorkflowID,
		)
	}
}

// isAskUserQuestion checks if the question metadata indicates an ask_user type
func isAskUserQuestion(metadata *string) bool {
	if metadata == nil {
		return false
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(*metadata), &m); err != nil {
		return false
	}
	if t, ok := m["type"].(string); ok {
		return t == "ask_user"
	}
	return false
}

// saveUserReplyMessage saves the user's reply as a user message in the question's thread
func (s *Service) saveUserReplyMessage(ctx context.Context, question *db.Question, responseData string) error {
	// Parse response data to get the user's reply text
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(responseData), &response); err != nil {
		// If not JSON, use the raw string as the reply
		_, err := s.deps.Repo.SaveMessageToThread(ctx, question.ChatID, question.ThreadID,
			int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), responseData, &question.WorkflowID, nil, nil)
		return err
	}

	replyText, _ := response["reply"].(string)
	if replyText == "" {
		replyText, _ = response["text"].(string)
	}
	if replyText == "" {
		// The web client's ask_user form submits {"answers":[{question,
		// selected, freetext}]} — the same shape parseQuestionResponse
		// (inline_workflow_executor.go) reads for has_feedback. The two must
		// agree: whenever the workflow sees feedback and loops into another
		// LLM call, a user message MUST exist in the thread, or the next
		// call fails with "conversation history ends with assistant
		// message" and retries to exhaustion.
		replyText = formatAnswersReply(responseData)
	}
	if replyText == "" {
		return nil
	}

	_, err := s.deps.Repo.SaveMessageToThread(ctx, question.ChatID, question.ThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), replyText, &question.WorkflowID, nil, nil)
	return err
}

// formatAnswersReply extracts a user-visible reply from the ask_user answers
// payload. Mirrors parseQuestionResponse's feedback semantics: a bare
// "Continue" selection with no freetext is not feedback and yields "" (no
// message saved — the workflow exits its loop without another LLM call).
// Unlike the workflow (which only consumes the first answer), all answered
// questions are preserved in the saved message.
func formatAnswersReply(responseData string) string {
	var payload struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &payload); err != nil || len(payload.Answers) == 0 {
		return ""
	}

	var parts []string
	for _, a := range payload.Answers {
		answer := a.Freetext
		if answer == "" {
			selected := make([]string, 0, len(a.Selected))
			for _, s := range a.Selected {
				if s != "Continue" {
					selected = append(selected, s)
				}
			}
			answer = strings.Join(selected, ", ")
		}
		if answer == "" {
			continue
		}
		if a.Question != "" && len(payload.Answers) > 1 {
			parts = append(parts, a.Question+": "+answer)
		} else {
			parts = append(parts, answer)
		}
	}
	return strings.Join(parts, "\n")
}

// stringPtrToString safely dereferences a string pointer
func stringPtrToString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
