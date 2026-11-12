package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectproto"
)

// ListSharedProjects lists the projects other members shared with an org
// the caller is a live member of, each decided by the viewer rule and in the
// viewer's shape (no path).
func (s *Service) ListSharedProjects(
	ctx context.Context,
	req *connect.Request[pb.ListSharedProjectsRequest],
) (*connect.Response[pb.ListSharedProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := auth.RequirePerson(ctx, "read what others shared with your organization"); err != nil {
		return nil, err
	}
	org, err := s.sharedOrg(ctx, userID, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}
	out := &pb.ListSharedProjectsResponse{Projects: []*pb.Project{}}
	if org == "" {
		return connect.NewResponse(out), nil
	}
	limit, offset := page(req.Msg.GetLimit(), req.Msg.GetOffset())
	projects, err := s.deps.Repo.ListSharedProjects(ctx, core.SharedProjectFilters{
		OrgID: org, ViewerUserID: userID, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list shared projects"))
	}
	ctx = authz.WithViewerReads(ctx)
	for _, p := range authz.SharedProjectsFor(ctx, userID, projects) {
		out.Projects = append(out.Projects, projectproto.ForViewer(projectproto.Project(p)))
	}
	return connect.NewResponse(out), nil
}
