package mcp

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// UninstallServer removes an MCP server
func (s *Service) UninstallServer(
	ctx context.Context,
	req *connect.Request[reliantv1.UninstallServerRequest],
) (*connect.Response[reliantv1.UninstallServerResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)
	checkout, err := s.projectCheckout(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	name := req.Msg.Name
	if name == "" {
		return nil, svcerr.InvalidArgument("server name is required")
	}

	if s.deps.Router == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "MCP not available")
	}

	// Remove from running manager first
	if err := s.mcpManagerFor(userID, checkout).RemoveProjectServer(checkout.Path, name); err != nil {
		logging.WarnContext(ctx, "Failed to remove MCP server from manager during uninstall", "error", err, "name", name)
		// Continue anyway to remove from config files
	}

	updatedScopes, err := s.removeServerFromAllScopes(ctx, projectID, userID, name)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to save MCP configuration", err)))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, updatedScopes); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	logging.InfoContext(ctx, "MCP server uninstalled successfully", "name", name)

	if err := s.deps.Refetch.EmitUserRefetch(ctx, userID, core.RefetchConfigHealth, core.RefetchOpts{ProjectID: &projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UninstallServer", "error", err)
	}

	return connect.NewResponse(&reliantv1.UninstallServerResponse{
		Success: true,
		Message: "Server uninstalled successfully",
		Name:    name,
	}), nil
}
