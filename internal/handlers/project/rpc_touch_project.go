package project

import (
	"context"
	"encoding/json"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

// TouchProject updates the project's last_active timestamp
func (s *Service) TouchProject(
	ctx context.Context,
	req *connect.Request[reliantv1.TouchProjectRequest],
) (*connect.Response[reliantv1.TouchProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	if err := s.deps.Repo.TouchProject(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project activity"))
	}

	if s.deps.Router != nil {
		project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			logging.WarnContext(ctx, "TouchProject: failed to load project for MCP warmup", "projectID", req.Msg.ProjectId, "error", err)
		} else if project != nil {
			// Warm up on the machine holding the checkout, at its path there.
			// A project no machine holds has nothing to warm up.
			target, err := s.checkout(ctx, userID, project, "load its MCP servers")
			if err != nil {
				logging.InfoContext(ctx, "TouchProject: no checkout to warm MCP servers on", "projectID", project.ID, "error", err)
				target = projectcheckout.Target{}
			}
			projectID := project.ID
			go func() {
				if target.DaemonID == "" {
					return
				}
				warmupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()

				// Send mcp.ensure_loaded daemon command to warmup MCP servers on the user's machine
				payload, _ := json.Marshal(map[string]string{"project_path": target.Path})
				if _, err := target.Send(warmupCtx, s.deps.Router, userID, "mcp.ensure_loaded", payload, 90000); err != nil {
					logging.WarnContext(ctx, "Project MCP warmup via daemon failed", "projectID", projectID, "error", err)
				}
			}()
		}
	}

	return connect.NewResponse(&reliantv1.TouchProjectResponse{
		Success: true,
		Message: "Project activity updated",
	}), nil
}
