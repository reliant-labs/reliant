package plan

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetPlan retrieves a plan by ID
func (s *Service) GetPlan(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPlanRequest],
) (*connect.Response[reliantv1.GetPlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	plan, chat, err := s.writablePlan(ctx, userID, req.Msg.PlanId)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetPlanResponse{Plan: planToProto(plan, chat.ID)}), nil
}
