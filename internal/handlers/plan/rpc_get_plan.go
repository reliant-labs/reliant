package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// GetPlan retrieves a plan by ID
func (s *Service) GetPlan(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPlanRequest],
) (*connect.Response[reliantv1.GetPlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	plan, err := s.deps.Repo.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	chatID, _ := s.getChatIDForPlan(ctx, plan)

	resp := &reliantv1.GetPlanResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    chatID,
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
