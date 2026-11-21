package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// UpdateProjectMetadata updates project metadata
func (s *Service) UpdateProjectMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateProjectMetadataRequest],
) (*connect.Response[reliantv1.UpdateProjectMetadataResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Update fields
	if req.Msg.Description != nil {
		project.Description = req.Msg.Description
	}

	if err := s.deps.Repo.UpdateProject(ctx, project, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project"))
	}

	return connect.NewResponse(&reliantv1.UpdateProjectMetadataResponse{
		Project: projectToProto(project),
	}), nil
}
