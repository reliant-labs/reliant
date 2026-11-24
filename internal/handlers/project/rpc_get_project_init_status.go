package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GetProjectInitStatus returns the initialization status of a project
func (s *Service) GetProjectInitStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectInitStatusRequest],
) (*connect.Response[reliantv1.GetProjectInitStatusResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Check initialization status via daemon (filesystem checks)
	var initStatus struct {
		Initialized bool   `json:"initialized"`
		Message     string `json:"message"`
	}
	// Asked of the machine holding a checkout, about its path there — never
	// projects.path, the creating machine's. With no checkout to ask, the
	// fallback below answers, as it does for a daemon that cannot.
	if target, terr := s.checkout(ctx, userID, project, "open it"); terr != nil {
		err = terr
	} else {
		initPayload := struct {
			Path      string `json:"path"`
			IsGitRepo bool   `json:"is_git_repo"`
		}{Path: target.Path, IsGitRepo: project.IsGitRepo}
		err = s.sendToDaemon(ctx, userID, target.DaemonID, "project.check_init_status", initPayload, &initStatus)
	}
	if err != nil {
		logging.ErrorContext(ctx, "Failed to check init status via daemon", "error", err, "projectID", req.Msg.ProjectId)
		// Fallback: assume initialized since it exists in the database
		initStatus.Initialized = true
		initStatus.Message = "Project is initialized"
	}

	return connect.NewResponse(&reliantv1.GetProjectInitStatusResponse{
		Initialized: initStatus.Initialized,
		ProjectId:   req.Msg.ProjectId,
		Message:     initStatus.Message,
	}), nil
}
