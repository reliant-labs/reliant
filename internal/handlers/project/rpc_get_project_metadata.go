package project

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetProjectMetadata returns detailed project metadata
func (s *Service) GetProjectMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectMetadataRequest],
) (*connect.Response[reliantv1.GetProjectMetadataResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	resp := &reliantv1.GetProjectMetadataResponse{
		ProjectId:  project.ID,
		Name:       project.Name,
		Path:       project.Path,
		IsGitRepo:  project.IsGitRepo,
		CreatedAt:  project.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  project.UpdatedAt.Format(time.RFC3339),
		LastActive: project.LastActive.Format(time.RFC3339),
	}
	if project.Description != nil {
		resp.Description = project.Description
	}
	if project.DefaultBranch != nil {
		resp.DefaultBranch = project.DefaultBranch
	}

	return connect.NewResponse(resp), nil
}
