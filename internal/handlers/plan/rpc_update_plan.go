package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// UpdatePlan updates an existing plan
func (s *Service) UpdatePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdatePlanRequest],
) (*connect.Response[reliantv1.UpdatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	plan, err := s.deps.Repo.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	chatID, _ := s.getChatIDForPlan(ctx, plan)

	// Update fields
	if req.Msg.Title != nil {
		plan.Title = *req.Msg.Title
	}
	if req.Msg.Description != nil {
		plan.Description = req.Msg.Description
	}
	if req.Msg.Status != nil {
		plan.Status = int32(*req.Msg.Status)
	}
	if req.Msg.Complexity != nil {
		plan.Complexity = planproto.PlanComplexityToInt32Ptr(req.Msg.Complexity)
	}

	if err := s.deps.Repo.UpdatePlan(ctx, plan); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update plan"))
	}

	if err := s.deps.Repo.EmitChatRefetch(ctx, chatID, db.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}

	resp := &reliantv1.UpdatePlanResponse{
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
