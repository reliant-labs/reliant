// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetWorkflowNodeExecutions returns the newest node_execution update of each
// node of one of the caller's chat's workflow runs.
//
// It is the start of a run-status view (the desktop Run status panel, the
// mobile workflow screen, the builder's test run): the chat snapshot does not
// carry node state, because a chat accumulates one entity per node of every run
// it ever made and the snapshot read all of them on every open, while only a
// view of ONE run reads any. The view then follows the chat stream, whose live
// node events are newer than anything returned here.
func (s *Service) GetWorkflowNodeExecutions(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorkflowNodeExecutionsRequest],
) (*connect.Response[reliantv1.GetWorkflowNodeExecutionsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	chatID, workflowID := req.Msg.GetChatId(), req.Msg.GetWorkflowId()
	if chatID == "" || workflowID == "" {
		return nil, svcerr.InvalidArgument("chat_id and workflow_id are required")
	}
	if err := s.requireChatOwner(ctx, chatID, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Scoped to the chat: a workflow id of another chat matches nothing.
	updates, err := s.deps.Repo.GetLatestWorkflowNodeUpdates(ctx, chatID, workflowID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the run's node executions"))
	}
	out := make([]*reliantv1.ChatUpdate, len(updates))
	for i, update := range updates {
		out[i] = &reliantv1.ChatUpdate{
			SequenceNumber: update.SequenceNumber,
			UpdateType:     update.UpdateType.String(),
			EntityId:       update.EntityID,
			Data:           string(update.Data),
			CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
		}
	}
	return connect.NewResponse(&reliantv1.GetWorkflowNodeExecutionsResponse{Updates: out}), nil
}
