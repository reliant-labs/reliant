// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
)

const (
	installServerStartupTimeout = 120 * time.Second
)

// mcpManagerRuntime starts and stops a user's MCP servers. What they are
// doing is read through Service.serverStatus instead.
type mcpManagerRuntime interface {
	AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error
	RemoveProjectServer(projectPath, serverName string) error
	RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error
}

// mcpManagerForUser returns an mcpManagerRuntime that proxies through the daemon
// for the given user. MCP servers run on the user's machine (tools daemon), so
// all runtime operations are dispatched via daemon commands.
func (s *Service) mcpManagerForUser(userID string) mcpManagerRuntime {
	proxy := newDaemonMCPProxy(s.deps.Router, userID)
	proxy.onChange = func() { s.statuses.invalidateUser(userID) }
	return proxy
}

// serverStatus is the project's MCP server status as the daemon last reported
// it, or nil when it is not known (no machine, or one too slow to answer in
// time). It never waits on the daemon for long: see mcpStatusCache.
func (s *Service) serverStatus(ctx context.Context, userID, projectPath string) *daemonMCPServerStatus {
	if s.deps.Router == nil || s.statuses == nil {
		return nil
	}
	return s.statuses.get(ctx, userID, projectPath)
}

// applyServerStatus fills in a server's runtime state from the daemon's
// report. An enabled server the daemon has never heard of is configured but
// not running; with no report at all its status is unknown (UNSPECIFIED),
// which the UI shows as not connected.
func applyServerStatus(server *reliantv1.MCPServer, status *daemonMCPServerStatus) {
	server.Connected = false
	server.Healthy = false
	if status == nil {
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNSPECIFIED
		return
	}
	entry, ok := status.entry(server.Name)
	if !ok {
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED
		return
	}
	server.Connected = entry.Connected
	server.Healthy = entry.Healthy
	switch {
	case entry.Connected && entry.Healthy:
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY
	case entry.Connected:
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNHEALTHY
	default:
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED
	}
	if info := entry.ServerInfo; info != nil {
		server.ServerInfo = &reliantv1.MCPServerInfo{Name: info.Name, Version: info.Version}
		server.ResourcesEnabled = info.Capabilities.Resources != nil
		server.PromptsEnabled = info.Capabilities.Prompts != nil
	} else {
		server.ResourcesEnabled = entry.ResourcesEnabled
		server.PromptsEnabled = entry.PromptsEnabled
	}
	server.ToolCount = int32(entry.ToolCount)
	if entry.LastError != "" {
		lastErr := entry.LastError
		server.LastError = &lastErr
	}
}

// mcpServerFromConfig is a server as configured, before any runtime status.
func mcpServerFromConfig(name string, scoped serverScopeConfig) *reliantv1.MCPServer {
	cfg := scoped.Config
	cfgType := cfg.Type
	if cfgType == "" {
		cfgType = string(config.MCPStdio)
	}
	server := &reliantv1.MCPServer{
		Name: name,
		Config: &reliantv1.MCPServerConfig{
			Command: cfg.Command,
			Args:    cfg.Args,
			Headers: cfg.Headers,
			Type:    cfgType,
		},
		Enabled: cfg.IsEnabled(),
		Scope:   scoped.Scope,
	}
	if cfg.URL != "" {
		server.Config.Url = &cfg.URL
	}
	for k, v := range cfg.Env {
		server.Config.Env = append(server.Config.Env, k+"="+v)
	}
	return server
}

// projectBelongsToUser checks if a project belongs to the authenticated user.
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if svcerr.IsNotFound(err) {
		return svcerr.NotFound("project")
	}
	return svcerr.WrapOr(err, svcerr.Internal("database error"))
}

func (s *Service) getProjectPath(ctx context.Context, projectID string, userID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", svcerr.InvalidArgument("project_id is required")
	}
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if svcerr.IsNotFound(err) || (err == nil && (project == nil || strings.TrimSpace(project.Path) == "")) {
		return "", svcerr.NotFound("project")
	}
	if err != nil {
		return "", svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	return project.Path, nil
}

func normalizeScope(scope reliantv1.ConfigScope) reliantv1.ConfigScope {
	if scope == reliantv1.ConfigScope_CONFIG_SCOPE_UNSPECIFIED {
		return reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	}
	return scope
}
