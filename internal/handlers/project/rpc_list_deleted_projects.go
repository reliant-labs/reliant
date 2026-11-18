package project

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// ListDeletedProjects lists the projects scheduled for deletion that the
// caller may restore ("Recently deleted", project_delete.go): their own, and
// those they deleted as an org admin, each decided again now.
func (s *Service) ListDeletedProjects(
	ctx context.Context,
	req *connect.Request[pb.ListDeletedProjectsRequest],
) (*connect.Response[pb.ListDeletedProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	rows, err := s.deps.Repo.ListDeletedProjects(ctx, userID, scope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list deleted projects"))
	}
	out := make([]*pb.Project, 0, len(rows))
	for _, p := range rows {
		authority, err := authz.ProjectDelete(ctx, userID, p)
		if errors.Is(err, authz.ErrMembershipUnavailable) {
			return nil, projectDeleteRefusal(ctx, err, "list")
		}
		if err != nil {
			continue
		}
		out = append(out, deletedProjectToProto(p, authority))
	}
	return connect.NewResponse(&pb.ListDeletedProjectsResponse{Projects: out}), nil
}
