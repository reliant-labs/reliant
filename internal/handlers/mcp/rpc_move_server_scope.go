package mcp

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

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
	checkout, err := s.projectCheckout(ctx, projectID, userID)
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
	if err := s.writeMCPConfigViaDaemon(ctx, checkout, targetScope, targetConfig); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to write target MCP configuration", err)))
	}

	delete(sourceConfig.MCPServers, name)
	if err := s.writeMCPConfigViaDaemon(ctx, checkout, sourceScope, sourceConfig); err != nil {
		// Best effort rollback: restore source entry if source write failed after target write.
		sourceConfig.MCPServers[name] = serverCfg
		_ = s.writeMCPConfigViaDaemon(ctx, checkout, sourceScope, sourceConfig)
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText(fmt.Sprintf("failed to finalize move from %s to %s", scoped.Scope.String(), targetScope.String()), err)))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		sourceScope: sourceConfig,
		targetScope: targetConfig,
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	if targetScope == reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL {
		s.ensureProjectLocalGitignore(ctx, checkout)
	}

	if s.deps.Router != nil {
		mgr := s.mcpManagerFor(userID, checkout)
		if err := mgr.RemoveProjectServer(checkout.Path, name); err != nil {
			logging.WarnContext(ctx, "Failed to remove server before restarting after scope move", "name", name, "error", err)
		}

		intCfg := scopedConfigToInternal(serverCfg)
		if intCfg.Enabled {
			startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
			defer cancel()

			if err := mgr.AddProjectServer(startCtx, checkout.Path, name, intCfg); err != nil {
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
