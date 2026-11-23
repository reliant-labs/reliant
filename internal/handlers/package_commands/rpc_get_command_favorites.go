package package_commands

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ============================================================================
// RPC Handlers
// ============================================================================

// GetCommandFavorites returns the list of favorited command keys for a project
func (s *Service) GetCommandFavorites(
	ctx context.Context,
	req *connect.Request[reliantv1.GetCommandFavoritesRequest],
) (*connect.Response[reliantv1.GetCommandFavoritesResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	// Verify project exists and belongs to user
	_, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Get favorites from database
	commandKeys, err := s.deps.Repo.ListCommandFavorites(ctx, userID, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list command favorites"))
	}

	return connect.NewResponse(&reliantv1.GetCommandFavoritesResponse{
		CommandKeys: commandKeys,
	}), nil
}
