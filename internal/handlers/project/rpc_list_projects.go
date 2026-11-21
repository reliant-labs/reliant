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

// ListProjects lists all projects for the current user
func (s *Service) ListProjects(
	ctx context.Context,
	req *connect.Request[reliantv1.ListProjectsRequest],
) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 100
	}
	offset := int(req.Msg.Offset)
	if offset < 0 {
		offset = 0
	}

	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{
		UserID: userID,
		Org:    scope,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		// A deadline or a cancellation is the client's to see as such, not a
		// server fault: only an unclassified store failure is Internal.
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}
	// A project of an org the caller has left is not listed, named org or
	// not: the list shows what GetProject would.
	projects = authz.Visible(ctx, userID, projects, authz.CanReadProject)

	protoProjects := make([]*reliantv1.Project, len(projects))
	for i, p := range projects {
		protoProjects[i] = projectToProto(p)
	}

	return connect.NewResponse(&reliantv1.ListProjectsResponse{
		Projects: protoProjects,
		Total:    int32(len(projects)),
	}), nil
}
