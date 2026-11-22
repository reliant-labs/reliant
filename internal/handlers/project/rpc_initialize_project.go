package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// InitializeProject initializes a project (no-op in V2)
func (s *Service) InitializeProject(
	ctx context.Context,
	req *connect.Request[reliantv1.InitializeProjectRequest],
) (*connect.Response[reliantv1.InitializeProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Project initialization is a no-op in V2 (projects are initialized on creation)
	return connect.NewResponse(&reliantv1.InitializeProjectResponse{
		ProjectId:   project.ID,
		Status:      "initialized",
		Message:     "Project already initialized",
		Initialized: true,
	}), nil
}
