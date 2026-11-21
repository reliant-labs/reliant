package question

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
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
	if !authz.CanWriteChat(ctx, userID, chat) {
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

// stringPtrToString safely dereferences a string pointer
func stringPtrToString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
