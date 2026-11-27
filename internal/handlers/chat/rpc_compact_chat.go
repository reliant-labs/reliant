package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// CompactChat triggers manual context compaction for a chat
func (s *Service) CompactChat(
	ctx context.Context,
	req *connect.Request[reliantv1.CompactChatRequest],
) (*connect.Response[reliantv1.CompactChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.ThreadId == "" {
		return nil, svcerr.InvalidArgument("thread_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.chatRows().GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanOwnChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// Check if there are any messages to compact on the specified thread
	threadToCompact := req.Msg.ThreadId
	messages, err := s.deps.Repo.ListMessages(ctx, req.Msg.ChatId, db.MessageListOptions{
		Thread: &threadToCompact,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check messages"))
	}

	if len(messages) == 0 {
		return nil, svcerr.FailedPrecondition("cannot compact: thread has no messages")
	}

	// Check if a workflow is already running - use Temporal as source of truth
	if workflowID := chat.MainThreadID(); workflowID != "" {
		temporalState, err := s.runs.State(ctx, workflowID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Unavailable("failed to check workflow status"))
		}
		if temporalState.Exists && temporalState.IsRunning {
			return nil, svcerr.FailedPrecondition("cannot compact while a workflow is running")
		}
	}

	// Start the compact workflow with a new UUID
	workflowID := workflow.NewWorkflowID()

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                workflow.SharedTaskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}

	// Compact workflow uses the specified thread
	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: "builtin://compact",
		Inputs: map[string]interface{}{
			"thread": threadToCompact,
		},
		ExecContext: &v2.ExecutionContext{
			WorkflowID:   workflowID,
			ChatID:       req.Msg.ChatId,
			WorkflowName: "builtin://compact",
			Thread:       threadToCompact,
			ThreadMode:   model.ThreadModeInherit,
		},
		Trigger: launch.LoadChatTrigger(ctx, s.triggerReads(), req.Msg.ChatId),
	}

	workflowRun, err := s.deps.Temporal.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start compaction"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)

	return connect.NewResponse(&reliantv1.CompactChatResponse{
		ChatId:     req.Msg.ChatId,
		WorkflowId: workflowID,
		RunId:      runID,
		Status:     "compacting",
		Message:    "Context compaction started",
	}), nil
}
