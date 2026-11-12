package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/projectproto"
)

// SetProjectVisibility is SetChatVisibility for a project. Sharing a project
// shares its card and lets viewers list its shared chats; it never shares a
// chat in it.
func (s *Service) SetProjectVisibility(
	ctx context.Context,
	req *connect.Request[pb.SetProjectVisibilityRequest],
) (*connect.Response[pb.SetProjectVisibilityResponse], error) {
	userID := auth.MustGetUserID(ctx)
	to, err := visibilityFrom(req.Msg.GetVisibility())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithViewerReads(ctx)
	project, err := s.deps.Repo.GetProject(ctx, req.Msg.GetProjectId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	change, err := authz.ProjectShareChange(ctx, userID, project, to)
	if err != nil {
		return nil, shareRefusal(ctx, "project", err)
	}
	changed := false
	if authz.ProjectVisibility(project) != to {
		if changed, err = s.deps.Repo.ApplyShareChange(ctx, change); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to change who can read the project"))
		}
	}
	resp := &pb.SetProjectVisibilityResponse{Changed: changed}
	if after, err := s.deps.Repo.GetProject(ctx, project.ID); err == nil && authz.CanReadProject(ctx, userID, after) {
		resp.Project = projectproto.Project(after)
		if authz.IsProjectViewer(ctx, userID, after) {
			resp.Project = projectproto.ForViewer(resp.Project)
		}
	}
	return connect.NewResponse(resp), nil
}
