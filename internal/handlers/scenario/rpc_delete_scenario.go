package scenario

import (
	"context"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// DeleteScenario deletes a scenario
func (s *Service) DeleteScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteScenarioRequest],
) (*connect.Response[reliantv1.DeleteScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, svcerr.Unauthenticated("user not authenticated")
	}

	// Verify the scenario belongs to the user
	scenario, err := s.deps.Repo.GetWorkflowScenario(ctx, req.Msg.ScenarioId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get scenario"))
	}
	if scenario == nil {
		return nil, svcerr.NotFound("scenario")
	}
	if !authz.CanAccessPersonal(userID, scenario.UserID) {
		return nil, svcerr.PermissionDenied("not authorized to delete this scenario")
	}

	if err := s.deps.Repo.DeleteWorkflowScenario(ctx, req.Msg.ScenarioId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete scenario"))
	}

	return connect.NewResponse(&reliantv1.DeleteScenarioResponse{
		Success: true,
		Message: "Scenario deleted successfully",
	}), nil
}
