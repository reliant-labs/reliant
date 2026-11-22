package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListProjectDaemonsForDaemon returns the project_daemons rows installed on
// the given daemon, filtered to projects owned by the calling user.
func (s *Service) ListProjectDaemonsForDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ListProjectDaemonsForDaemonRequest],
) (*connect.Response[reliantv1.ListProjectDaemonsForDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.DaemonId == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}

	rows, err := s.deps.Installs.ListProjectDaemonsForDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project_daemons"))
	}

	// Filter by ownership: a project_daemons row is only visible to a user
	// if the underlying project belongs to them.
	out := make([]*reliantv1.ProjectDaemon, 0, len(rows))
	names := s.newMachineNames()
	for _, pd := range rows {
		if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, pd.ProjectID, userID); err != nil {
			continue
		}
		out = append(out, names.proto(ctx, pd))
	}

	return connect.NewResponse(&reliantv1.ListProjectDaemonsForDaemonResponse{
		ProjectDaemons: out,
	}), nil
}
