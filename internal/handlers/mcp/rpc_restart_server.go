package mcp

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// RestartServer restarts an MCP server
func (s *Service) RestartServer(
	ctx context.Context,
	req *connect.Request[reliantv1.RestartServerRequest],
) (*connect.Response[reliantv1.RestartServerResponse], error) {
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

	mgr := s.mcpManagerFor(userID, checkout)

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	scoped, found := scopedServers[name]
	if !found {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "server not found in configuration")
	}

	if err := mgr.RemoveProjectServer(checkout.Path, name); err != nil {
		logging.WarnContext(ctx, "RestartServer: failed to remove existing server before restart", "name", name, "error", err)
	}

	if !scoped.Config.IsEnabled() {
		return connect.NewResponse(&reliantv1.RestartServerResponse{
			Success: true,
			Message: "Server is disabled; no restart performed",
			Name:    name,
		}), nil
	}

	intCfg := scopedConfigToInternal(scoped.Config)

	if err := s.preflightMCPServer(ctx, checkout, name, intCfg); err != nil {
		logServerFailure(ctx, "MCP server restart failed", err,
			"operation", "restart",
			"server", name,
			"phase", "preflight",
		)
		return connect.NewResponse(&reliantv1.RestartServerResponse{
			Success: false,
			Message: fmt.Sprintf("Preflight failed: %v", err),
			Name:    name,
		}), nil
	}

	startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
	defer cancel()

	if err := mgr.AddProjectServer(startCtx, checkout.Path, name, intCfg); err != nil {
		logServerFailure(ctx, "MCP server restart failed", err,
			"operation", "restart",
			"server", name,
			"phase", "startup",
		)
		return connect.NewResponse(&reliantv1.RestartServerResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to restart server: %v", err),
			Name:    name,
		}), nil
	}

	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
			logging.WarnContext(ctx, "Failed to emit config_health refetch after RestartServer", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.RestartServerResponse{
		Success: true,
		Message: "Server restarted successfully",
		Name:    name,
	}), nil
}
