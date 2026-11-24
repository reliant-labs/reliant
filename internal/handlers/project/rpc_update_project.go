package project

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// UpdateProject updates a project
func (s *Service) UpdateProject(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateProjectRequest],
) (*connect.Response[reliantv1.UpdateProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Update fields
	if req.Msg.Name != nil {
		project.Name = *req.Msg.Name
	}
	if req.Msg.Description != nil {
		project.Description = req.Msg.Description
	}
	if req.Msg.DefaultBranch != nil {
		project.DefaultBranch = req.Msg.DefaultBranch
	}

	project.UpdatedAt = time.Now().UTC()
	project.LastActive = time.Now().UTC()

	if err := s.deps.Repo.UpdateProject(ctx, project, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project"))
	}

	return connect.NewResponse(&reliantv1.UpdateProjectResponse{
		Project: projectToProto(project),
	}), nil
}
