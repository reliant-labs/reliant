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

// UpdateServerConfig updates the configuration of an MCP server
func (s *Service) UpdateServerConfig(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateServerConfigRequest],
) (*connect.Response[reliantv1.UpdateServerConfigResponse], error) {
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

	logging.DebugContext(ctx, "UpdateServerConfig request", "project_id", projectID, "name", name, "env_keys", len(req.Msg.Env))

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

	if serverCfg.Env == nil {
		serverCfg.Env = make(map[string]string)
	}
	for k, v := range req.Msg.Env {
		if v != "" {
			serverCfg.Env[k] = v
		}
	}
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
		if err := mgr.RemoveProjectServer(checkout.Path, name); err != nil {
			logging.WarnContext(ctx, "Failed to remove server before restart", "error", err)
		}

		intCfg := scopedConfigToInternal(serverCfg)
		if intCfg.Enabled {
			if err := mgr.AddProjectServer(ctx, checkout.Path, name, intCfg); err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText(fmt.Sprintf("configuration saved to %s scope but failed to restart server", scoped.Scope.String()), err)))
			}
		}
	}

	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
			logging.WarnContext(ctx, "Failed to emit config_health refetch after UpdateServerConfig", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.UpdateServerConfigResponse{Success: true, Message: "Configuration updated successfully"}), nil
}
