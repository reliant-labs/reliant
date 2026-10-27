package mcp

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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
	if err := s.mcpManagerForUser(userID).RemoveProjectServer(projectPath, name); err != nil {
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

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UninstallServer", "error", err)
	}

	return connect.NewResponse(&reliantv1.UninstallServerResponse{
		Success: true,
		Message: "Server uninstalled successfully",
		Name:    name,
	}), nil
}

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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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

	mgr := s.mcpManagerForUser(userID)

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	scoped, found := scopedServers[name]
	if !found {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "server not found in configuration")
	}

	if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
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

	if err := s.preflightMCPServer(ctx, name, intCfg); err != nil {
		logging.ErrorContext(ctx, "MCP server restart failed",
			"operation", "restart",
			"server", name,
			"phase", "preflight",
			"error", err,
		)
		return connect.NewResponse(&reliantv1.RestartServerResponse{
			Success: false,
			Message: fmt.Sprintf("Preflight failed: %v", err),
			Name:    name,
		}), nil
	}

	startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
	defer cancel()

	if err := mgr.AddProjectServer(startCtx, projectPath, name, intCfg); err != nil {
		logging.ErrorContext(ctx, "MCP server restart failed",
			"operation", "restart",
			"server", name,
			"phase", "startup",
			"error", err,
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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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

	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scoped.Scope, mcpConfig); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to save MCP configuration", err)))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		scoped.Scope: mcpConfig,
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	if s.deps.Router != nil {
		mgr := s.mcpManagerForUser(userID)
		if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
			logging.WarnContext(ctx, "Failed to remove server before restart", "error", err)
		}

		intCfg := scopedConfigToInternal(serverCfg)
		if intCfg.Enabled {
			if err := mgr.AddProjectServer(ctx, projectPath, name, intCfg); err != nil {
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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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

	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scoped.Scope, mcpConfig); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to save MCP configuration", err)))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		scoped.Scope: mcpConfig,
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	if s.deps.Router != nil {
		mgr := s.mcpManagerForUser(userID)
		if req.Msg.Enabled {
			if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
				logging.WarnContext(ctx, "Failed to remove server before enable restart", "name", name, "error", err)
			}

			intCfg := scopedConfigToInternal(serverCfg)
			startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
			defer cancel()

			if err := s.preflightMCPServer(ctx, name, intCfg); err != nil {
				logging.ErrorContext(ctx, "MCP server enable/disable failed",
					"operation", "set_enabled",
					"server", name,
					"phase", "preflight",
					"error", err,
				)
				return connect.NewResponse(&reliantv1.SetServerEnabledResponse{
					Success: false,
					Message: fmt.Sprintf("Server enabled in config, but preflight failed: %v", err),
					Name:    name,
					Enabled: true,
				}), nil
			}

			if err := mgr.AddProjectServer(startCtx, projectPath, name, intCfg); err != nil {
				logging.ErrorContext(ctx, "MCP server enable/disable failed",
					"operation", "set_enabled",
					"server", name,
					"phase", "startup",
					"error", err,
				)
				return connect.NewResponse(&reliantv1.SetServerEnabledResponse{
					Success: false,
					Message: fmt.Sprintf("Server enabled in config, but failed to start: %v", err),
					Name:    name,
					Enabled: true,
				}), nil
			}
		} else {
			if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
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

// MoveServerScope moves a server configuration between scopes.
func (s *Service) MoveServerScope(
	ctx context.Context,
	req *connect.Request[reliantv1.MoveServerScopeRequest],
) (*connect.Response[reliantv1.MoveServerScopeResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	name := req.Msg.Name
	if name == "" {
		return nil, svcerr.InvalidArgument("server name is required")
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	targetScope := normalizeScope(req.Msg.Scope)

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	scoped, exists := scopedServers[name]
	if !exists {
		return nil, svcerr.NotFound("server")
	}
	sourceScope := normalizeScope(scoped.Scope)

	if scoped.Scope == targetScope {
		activity.Skip(ctx) // already stored there: nothing moved
		return connect.NewResponse(&reliantv1.MoveServerScopeResponse{
			Success: true,
			Message: "Server is already stored in the requested scope",
			Name:    name,
			Scope:   targetScope,
		}), nil
	}

	sourceConfig, err := s.readMCPConfigForScope(ctx, projectID, sourceScope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read source MCP configuration"))
	}

	targetConfig, err := s.readMCPConfigForScope(ctx, projectID, targetScope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read target MCP configuration"))
	}

	serverCfg, sourceExists := sourceConfig.MCPServers[name]
	if !sourceExists {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "server not found in source scope")
	}

	targetConfig.MCPServers[name] = serverCfg
	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, targetScope, targetConfig); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to write target MCP configuration", err)))
	}

	delete(sourceConfig.MCPServers, name)
	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, sourceScope, sourceConfig); err != nil {
		// Best effort rollback: restore source entry if source write failed after target write.
		sourceConfig.MCPServers[name] = serverCfg
		_ = s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, sourceScope, sourceConfig)
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText(fmt.Sprintf("failed to finalize move from %s to %s", scoped.Scope.String(), targetScope.String()), err)))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		sourceScope: sourceConfig,
		targetScope: targetConfig,
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	if targetScope == reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL {
		s.ensureProjectLocalGitignore(ctx, projectID)
	}

	if s.deps.Router != nil {
		mgr := s.mcpManagerForUser(userID)
		if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
			logging.WarnContext(ctx, "Failed to remove server before restarting after scope move", "name", name, "error", err)
		}

		intCfg := scopedConfigToInternal(serverCfg)
		if intCfg.Enabled {
			startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
			defer cancel()

			if err := mgr.AddProjectServer(startCtx, projectPath, name, intCfg); err != nil {
				logging.ErrorContext(ctx, "MCP server scope move failed",
					"operation", "move_scope",
					"server", name,
					"target_scope", targetScope.String(),
					"error", err,
				)
				return connect.NewResponse(&reliantv1.MoveServerScopeResponse{
					Success: false,
					Message: fmt.Sprintf("Server moved to %s, but failed to start: %v", targetScope.String(), err),
					Name:    name,
					Scope:   targetScope,
				}), nil
			}
		}
	}

	return connect.NewResponse(&reliantv1.MoveServerScopeResponse{
		Success: true,
		Message: fmt.Sprintf("Server moved to %s scope", targetScope.String()),
		Name:    name,
		Scope:   targetScope,
	}), nil
}
