package package_commands

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// SetCommandFavorite adds or removes a command from favorites
func (s *Service) SetCommandFavorite(
	ctx context.Context,
	req *connect.Request[reliantv1.SetCommandFavoriteRequest],
) (*connect.Response[reliantv1.SetCommandFavoriteResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	commandKey := req.Msg.CommandKey
	if commandKey == "" {
		return nil, svcerr.InvalidArgument("command_key is required")
	}

	// Verify project exists and belongs to user
	_, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Add or remove favorite based on is_favorite flag
	if req.Msg.IsFavorite {
		err = s.deps.Repo.AddCommandFavorite(ctx, userID, projectID, commandKey)
	} else {
		err = s.deps.Repo.RemoveCommandFavorite(ctx, userID, projectID, commandKey)
	}

	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update command favorite"))
	}

	return connect.NewResponse(&reliantv1.SetCommandFavoriteResponse{
		Success: true,
	}), nil
}
