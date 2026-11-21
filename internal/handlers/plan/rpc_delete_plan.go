package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DeletePlan deletes a plan
func (s *Service) DeletePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.DeletePlanRequest],
) (*connect.Response[reliantv1.DeletePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	// Get chatID before deleting
	plan, err := s.deps.Repo.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	chatID, _ := s.getChatIDForPlan(ctx, plan)

	if err := s.deps.Repo.DeletePlan(ctx, req.Msg.PlanId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete plan"))
	}

	if err := s.deps.Repo.EmitChatRefetch(ctx, chatID, db.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}

	return connect.NewResponse(&reliantv1.DeletePlanResponse{Success: true}), nil
}
