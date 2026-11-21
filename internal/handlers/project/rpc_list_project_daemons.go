package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// ListProjectDaemons returns every project_daemons row across all the
// caller's projects. Implemented as a fan-out over the user's projects (we
// already have ListProjectDaemonsForProject) rather than a new join query —
// the fan-out is bounded by project count (small) and keeps the SQL surface
// minimal. The picker uses this to tell users *which* daemon hosts a project
// instead of just "another one".
func (s *Service) ListProjectDaemons(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListProjectDaemonsRequest],
) (*connect.Response[reliantv1.ListProjectDaemonsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project_daemons"))
	}
	// Checkouts of a project the caller may no longer read (an org they
	// left, a credential's other org) are not listed.
	projects = authz.Visible(ctx, userID, projects, authz.CanReadProject)

	out := make([]*reliantv1.ProjectDaemon, 0)
	names := s.newMachineNames()
	for _, p := range projects {
		rows, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, p.ID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project_daemons"))
		}
		for _, pd := range rows {
			out = append(out, names.proto(ctx, pd))
		}
	}

	return connect.NewResponse(&reliantv1.ListProjectDaemonsResponse{
		ProjectDaemons: out,
	}), nil
}
