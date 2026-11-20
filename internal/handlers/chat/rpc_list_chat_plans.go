package chat

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// ListChatPlans lists plans associated with a chat
func (s *Service) ListChatPlans(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatPlansRequest],
) (*connect.Response[reliantv1.ListChatPlansResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat (authz.WithViewerReads).
	ctx = authz.WithViewerReads(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// The owner, or a viewer of a shared chat.
	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if _, denied := authz.ReadChatAccess(ctx, userID, chat); denied != nil {
		return nil, svcerr.NotFound("chat")
	}

	// List plans for the chat (across all threads)
	plans, err := s.deps.Repo.ListPlansByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list plans"))
	}

	// Convert to proto messages
	protoPlans := make([]*reliantv1.ChatPlan, len(plans))
	for i, p := range plans {
		protoPlan := &reliantv1.ChatPlan{
			Id:        p.ID,
			ChatId:    req.Msg.ChatId,
			Title:     p.Title,
			Status:    planproto.PlanStatus(p.Status),
			CreatedAt: p.CreatedAt.Format(time.RFC3339),
			UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		}
		if p.Description != nil {
			protoPlan.Description = p.Description
		}
		if p.CompletedAt != nil {
			completedAt := p.CompletedAt.Format(time.RFC3339)
			protoPlan.CompletedAt = &completedAt
		}
		protoPlans[i] = protoPlan
	}

	return connect.NewResponse(&reliantv1.ListChatPlansResponse{
		Plans: protoPlans,
		Total: int32(len(plans)),
	}), nil
}
