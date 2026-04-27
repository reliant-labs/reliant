// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// QuestionService implements the QuestionService RPC handlers
type QuestionService struct {
	reliantv1connect.UnimplementedQuestionServiceHandler
	database     db.Repository
	pauseService *workflow.PauseService
}

// NewQuestionService creates a new QuestionService
func NewQuestionService(database db.Repository, pauseService *workflow.PauseService) *QuestionService {
	return &QuestionService{
		database:     database,
		pauseService: pauseService,
	}
}

// ResolveQuestion resolves a pending question with the user's answer
func (s *QuestionService) ResolveQuestion(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveQuestionRequest],
) (*connect.Response[reliantv1.ResolveQuestionResponse], error) {
	if req.Msg.QuestionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("question_id is required"))
	}
	if req.Msg.Action == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action is required"))
	}

	// Look up the question
	question, err := s.database.GetQuestionByID(ctx, req.Msg.QuestionId)
	if err != nil || question == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("question not found"))
	}

	if question.Status != db.QuestionStatusPending {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("question already resolved"))
	}

	// Resolve the question in the database
	var responseData *string
	if req.Msg.ResponseData != nil && *req.Msg.ResponseData != "" {
		responseData = req.Msg.ResponseData
	}

	if err := s.database.ResolveQuestion(ctx, question.ID, responseData); err != nil {
		logging.Error("Failed to resolve question", "error", err, "questionID", question.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve question"))
	}

	// Emit question update (resolved)
	if err := s.database.EmitQuestionUpdate(ctx, question.ChatID, db.QuestionUpdate{
		QuestionID: question.ID,
		ChatID:     question.ChatID,
		WorkflowID: question.WorkflowID,
		StepID:     question.StepID,
		Status:     "resolved",
	}); err != nil {
		logging.Warn("[Question] Failed to emit question update", "error", err)
	}

	// For "reply" action with response_data and ask_user metadata: save user message to thread
	if req.Msg.Action == "reply" && responseData != nil && question.Metadata != nil {
		if isAskUserQuestion(question.Metadata) {
			if err := s.saveUserReplyMessage(ctx, question, *responseData); err != nil {
				logging.Warn("[Question] Failed to save user reply message", "error", err, "questionID", question.ID)
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

// GetPendingQuestion returns the current pending question for a chat
func (s *QuestionService) GetPendingQuestion(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPendingQuestionRequest],
) (*connect.Response[reliantv1.GetPendingQuestionResponse], error) {
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	question, err := s.database.GetPendingQuestionByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		// No pending question is not an error
		return connect.NewResponse(&reliantv1.GetPendingQuestionResponse{}), nil
	}

	if question == nil {
		return connect.NewResponse(&reliantv1.GetPendingQuestionResponse{}), nil
	}

	return connect.NewResponse(&reliantv1.GetPendingQuestionResponse{
		Question: questionToProto(question),
	}), nil
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
		StepId:     q.StepID,
		Status:     questionStatusString(q.Status),
		CreatedAt:  q.CreatedAt.Format(time.RFC3339),
	}
	if q.Metadata != nil {
		info.Metadata = q.Metadata
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
func (s *QuestionService) signalQuestion(ctx context.Context, question *db.Question, signalData map[string]interface{}) {
	if question.TemporalWorkflowID == "" {
		logging.Warn("[Question] No temporal_workflow_id on question, cannot signal", "questionID", question.ID)
		return
	}
	signalName := "signal.question." + question.ID
	if err := s.pauseService.SignalWithRecovery(ctx, question.TemporalWorkflowID, signalName, signalData); err != nil {
		logging.Warn("[Question] Failed to signal question resolution",
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
func (s *QuestionService) saveUserReplyMessage(ctx context.Context, question *db.Question, responseData string) error {
	// Parse response data to get the user's reply text
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(responseData), &response); err != nil {
		// If not JSON, use the raw string as the reply
		_, err := s.database.SaveMessageToThread(ctx, question.ChatID, question.ThreadID,
			int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), responseData, &question.WorkflowID, nil, nil)
		return err
	}

	replyText, _ := response["reply"].(string)
	if replyText == "" {
		replyText, _ = response["text"].(string)
	}
	if replyText == "" {
		return nil
	}

	_, err := s.database.SaveMessageToThread(ctx, question.ChatID, question.ThreadID,
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
