package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// GetPlanByChatId retrieves a plan by chat ID
func (s *Service) GetPlanByChatId(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPlanByChatIdRequest],
) (*connect.Response[reliantv1.GetPlanByChatIdResponse], error) {
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
		return nil, svcerr.PermissionDenied("access denied")
	}

	// Get plans for this chat (via thread join)
	plans, err := s.deps.Repo.ListPlansByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	// If no plans found, return empty response
	if len(plans) == 0 {
		return connect.NewResponse(&reliantv1.GetPlanByChatIdResponse{}), nil
	}

	// Return the first plan
	plan := plans[0]
	resp := &reliantv1.GetPlanByChatIdResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    req.Msg.ChatId,
			Title:     plan.Title,
			Status:    planproto.PlanStatus(plan.Status),
			CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if plan.Description != nil {
		resp.Plan.Description = *plan.Description
	}
	if plan.Complexity != nil {
		resp.Plan.Complexity = planproto.PlanComplexity(*plan.Complexity)
	}

	return connect.NewResponse(resp), nil
}
