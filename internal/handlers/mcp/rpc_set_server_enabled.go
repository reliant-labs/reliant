package mcp

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// SetServerEnabled toggles whether a server is enabled without uninstalling.
func (s *Service) SetServerEnabled(
	ctx context.Context,
	req *connect.Request[reliantv1.SetServerEnabledRequest],
) (*connect.Response[reliantv1.SetServerEnabledResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	name := req.Msg.Name
	if name == "" {
		return nil, svcerr.InvalidArgument("server name is required")
	}

	userID := auth.MustGetUserID(ctx)
	checkout, err := s.projectCheckout(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	scoped, exists := scopedServers[name]
	if !exists {
		return nil, svcerr.NotFound("server")
	}

	mcpConfig, err := s.readMCPConfigForScope(ctx, projectID, scoped.Scope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	serverCfg, existsInTargetScope := mcpConfig.MCPServers[name]
	if !existsInTargetScope {
		serverCfg = scoped.Config
	}
	serverCfg.Enabled = ptr.Of(req.Msg.Enabled)
	mcpConfig.MCPServers[name] = serverCfg

	if err := s.writeMCPConfigViaDaemon(ctx, checkout, scoped.Scope, mcpConfig); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to save MCP configuration", err)))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		scoped.Scope: mcpConfig,
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	if s.deps.Router != nil {
		mgr := s.mcpManagerFor(userID, checkout)
		if req.Msg.Enabled {
			if err := mgr.RemoveProjectServer(checkout.Path, name); err != nil {
				logging.WarnContext(ctx, "Failed to remove server before enable restart", "name", name, "error", err)
			}

			intCfg := scopedConfigToInternal(serverCfg)
			startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
			defer cancel()

			if err := s.preflightMCPServer(ctx, checkout, name, intCfg); err != nil {
				logServerFailure(ctx, "MCP server enable/disable failed", err,
					"operation", "set_enabled",
					"server", name,
					"phase", "preflight",
				)
				return connect.NewResponse(&reliantv1.SetServerEnabledResponse{
					Success: false,
					Message: fmt.Sprintf("Server enabled in config, but preflight failed: %v", err),
					Name:    name,
					Enabled: true,
				}), nil
			}

			if err := mgr.AddProjectServer(startCtx, checkout.Path, name, intCfg); err != nil {
				logServerFailure(ctx, "MCP server enable/disable failed", err,
					"operation", "set_enabled",
					"server", name,
					"phase", "startup",
				)
				return connect.NewResponse(&reliantv1.SetServerEnabledResponse{
					Success: false,
					Message: fmt.Sprintf("Server enabled in config, but failed to start: %v", err),
					Name:    name,
					Enabled: true,
				}), nil
			}
		} else {
			if err := mgr.RemoveProjectServer(checkout.Path, name); err != nil {
				logging.WarnContext(ctx, "Failed to stop server while disabling", "name", name, "error", err)
			}
		}
	}

	stateText := "disabled"
	if req.Msg.Enabled {
		stateText = "enabled"
	}

	return connect.NewResponse(&reliantv1.SetServerEnabledResponse{
		Success: true,
		Message: fmt.Sprintf("Server %s", stateText),
		Name:    name,
		Enabled: req.Msg.Enabled,
	}), nil
}
