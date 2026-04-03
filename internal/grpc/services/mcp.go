// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cmdutil"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	installServerStartupTimeout = 120 * time.Second
)

type mcpManagerRuntime interface {
	AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error
	RemoveProjectServer(projectPath, serverName string) error
	RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error
	GetProjectClients(projectPath string) map[string]mcp.Client
	GetProjectHealthStatus(projectPath string) map[string]bool
	GetProjectLastError(projectPath, serverName string) error
	GetProjectClient(projectPath, serverName string) (mcp.Client, bool)
	AddServer(ctx context.Context, name string, cfg config.MCPServer) error
	RemoveServer(name string) error
	GetAllClients() map[string]mcp.Client
	GetHealthStatus() map[string]bool
	GetLastError(name string) error
	GetClient(name string) (mcp.Client, bool)
}

// MCPService implements the MCPService RPC handlers
type MCPService struct {
	reliantv1connect.UnimplementedMCPServiceHandler
	database     db.Repository
	daemonRouter toolexec.DaemonRouter
}

// NewMCPService creates a new MCPService
func NewMCPService(database db.Repository, daemonRouter toolexec.DaemonRouter) *MCPService {
	return &MCPService{
		database:     database,
		daemonRouter: daemonRouter,
	}
}

// mcpManagerForUser returns an mcpManagerRuntime that proxies through the daemon
// for the given user. MCP servers run on the user's machine (tools daemon), so
// all runtime operations are dispatched via daemon commands.
func (s *MCPService) mcpManagerForUser(userID string) mcpManagerRuntime {
	return NewDaemonMCPProxy(s.daemonRouter, userID)
}

// ============================================================================
// Helper Types
// ============================================================================

// MCPConfigFile represents the structure of .mcp.json
type MCPConfigFile struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// MCPServerConfig represents a server config in .mcp.json format
type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

func (c MCPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type serverScopeConfig struct {
	Config MCPServerConfig
	Scope  reliantv1.ConfigScope
}

func scopeToStoredMCPKey(scope reliantv1.ConfigScope) string {
	switch scope {
	case reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL:
		return "user"
	case reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL:
		return "local"
	case reliantv1.ConfigScope_CONFIG_SCOPE_UNSPECIFIED, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT:
		fallthrough
	default:
		return "project"
	}
}

func readMergedScopedServersFromStoredRecord(record *db.ProjectConfigRecord) map[string]serverScopeConfig {
	merged := make(map[string]serverScopeConfig)
	if record == nil || record.MCPConfigs == nil || strings.TrimSpace(*record.MCPConfigs) == "" {
		return merged
	}

	var scopeConfigs map[string]string
	if err := json.Unmarshal([]byte(*record.MCPConfigs), &scopeConfigs); err != nil {
		logging.Warn("Failed to parse stored MCP configs JSON", "error", err)
		return merged
	}

	for _, scopeName := range []string{"user", "project", "local"} {
		raw, ok := scopeConfigs[scopeName]
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}

		var cfg MCPConfigFile
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			logging.Warn("Failed to parse stored scoped MCP config", "scope", scopeName, "error", err)
			continue
		}
		if cfg.MCPServers == nil {
			continue
		}

		scope := reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
		switch scopeName {
		case "user":
			scope = reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL
		case "project":
			scope = reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
		case "local":
			scope = reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL
		}

		for name, serverCfg := range cfg.MCPServers {
			merged[name] = serverScopeConfig{Config: serverCfg, Scope: scope}
		}
	}

	return merged
}

func readScopeConfigFromStoredRecord(record *db.ProjectConfigRecord, scope reliantv1.ConfigScope) (*MCPConfigFile, bool, error) {
	if record == nil || record.MCPConfigs == nil || strings.TrimSpace(*record.MCPConfigs) == "" {
		return nil, false, nil
	}

	var scopeConfigs map[string]string
	if err := json.Unmarshal([]byte(*record.MCPConfigs), &scopeConfigs); err != nil {
		return nil, false, fmt.Errorf("failed to parse stored MCP configs JSON: %w", err)
	}

	raw, ok := scopeConfigs[scopeToStoredMCPKey(scope)]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, false, nil
	}

	var cfg MCPConfigFile
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, false, fmt.Errorf("failed to parse stored scoped MCP config: %w", err)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]MCPServerConfig)
	}

	return &cfg, true, nil
}

func (s *MCPService) buildUpdatedStoredMCPConfigs(existing *string, scope reliantv1.ConfigScope, updatedScopeCfg *MCPConfigFile) (*string, error) {
	scopeConfigs := make(map[string]string)
	if existing != nil && strings.TrimSpace(*existing) != "" {
		if err := json.Unmarshal([]byte(*existing), &scopeConfigs); err != nil {
			return nil, fmt.Errorf("failed to parse existing stored MCP configs: %w", err)
		}
	}

	if updatedScopeCfg == nil {
		return existing, nil
	}

	encodedScopeCfg, err := json.Marshal(updatedScopeCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode updated scope MCP config: %w", err)
	}
	scopeConfigs[scopeToStoredMCPKey(scope)] = string(encodedScopeCfg)

	flattened, err := json.Marshal(scopeConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to encode stored MCP configs snapshot: %w", err)
	}

	value := string(flattened)
	return &value, nil
}

func (s *MCPService) persistStoredMCPConfigScopes(
	ctx context.Context,
	projectID string,
	updatedScopes map[reliantv1.ConfigScope]*MCPConfigFile,
) error {
	if s.database == nil || len(updatedScopes) == 0 {
		return nil
	}

	if globalCfg, hasGlobal := updatedScopes[reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL]; hasGlobal && globalCfg != nil {
		if err := s.persistGlobalScopeSnapshotAcrossProjects(ctx, projectID, globalCfg); err != nil {
			return err
		}
		delete(updatedScopes, reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL)
	}

	if len(updatedScopes) == 0 {
		return nil
	}

	record, err := s.database.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("failed to load stored MCP configuration snapshot: %w", err)
	}
	if record == nil {
		return nil
	}

	updatedMCPConfigs := record.MCPConfigs
	for _, scope := range []reliantv1.ConfigScope{
		reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL,
		reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
		reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL,
	} {
		scopeCfg, ok := updatedScopes[scope]
		if !ok {
			continue
		}

		updatedMCPConfigs, err = s.buildUpdatedStoredMCPConfigs(updatedMCPConfigs, scope, scopeCfg)
		if err != nil {
			return fmt.Errorf("failed to update stored MCP configuration snapshot: %w", err)
		}
	}

	record.MCPConfigs = updatedMCPConfigs
	record.PushedAt = time.Now().UTC()
	if err := s.database.UpsertProjectConfigRecord(ctx, record); err != nil {
		return fmt.Errorf("failed to persist stored MCP configuration snapshot: %w", err)
	}

	return nil
}

func (s *MCPService) persistGlobalScopeSnapshotAcrossProjects(
	ctx context.Context,
	sourceProjectID string,
	globalCfg *MCPConfigFile,
) error {
	if s == nil || s.database == nil || globalCfg == nil {
		return nil
	}

	sourceProject, err := s.database.GetProject(ctx, sourceProjectID)
	if err != nil {
		return fmt.Errorf("failed to resolve source project for global MCP snapshot propagation: %w", err)
	}
	if sourceProject == nil || strings.TrimSpace(sourceProject.UserID) == "" {
		return nil
	}

	projects, err := s.database.ListProjects(ctx, db.ProjectFilters{
		UserID: sourceProject.UserID,
		Limit:  100000,
		Offset: 0,
	})
	if err != nil {
		return fmt.Errorf("failed to list projects for global MCP snapshot propagation: %w", err)
	}

	for _, project := range projects {
		if project == nil || strings.TrimSpace(project.ID) == "" {
			continue
		}

		record, recErr := s.database.GetProjectConfigRecord(ctx, project.ID)
		if recErr != nil {
			if errors.Is(recErr, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("failed to load project config record for global MCP propagation (project_id=%s): %w", project.ID, recErr)
		}

		updatedMCPConfigs, buildErr := s.buildUpdatedStoredMCPConfigs(record.MCPConfigs, reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, globalCfg)
		if buildErr != nil {
			return fmt.Errorf("failed to update global MCP snapshot for project %s: %w", project.ID, buildErr)
		}

		record.MCPConfigs = updatedMCPConfigs
		record.PushedAt = time.Now().UTC()
		if err := s.database.UpsertProjectConfigRecord(ctx, record); err != nil {
			return fmt.Errorf("failed to persist propagated global MCP snapshot for project %s: %w", project.ID, err)
		}
	}

	return nil
}

// ============================================================================
// Helper Methods
// ============================================================================

// scopeToDaemonWriteScope returns the daemon-side scope string for a config scope.
func scopeToDaemonWriteScope(scope reliantv1.ConfigScope) string {
	switch scope {
	case reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL:
		return "global"
	case reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL:
		return "local"
	case reliantv1.ConfigScope_CONFIG_SCOPE_UNSPECIFIED, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT:
		fallthrough
	default:
		return "project"
	}
}

// readMCPConfigForScope reads the MCP config for a specific scope from the DB-backed ProjectConfigRecord.
func (s *MCPService) readMCPConfigForScope(ctx context.Context, projectID string, scope reliantv1.ConfigScope) (*MCPConfigFile, error) {
	normalizedScope := normalizeScope(scope)

	if s.database == nil {
		return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
	}

	record, err := s.database.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
		}
		return nil, fmt.Errorf("failed to load stored MCP configuration snapshot: %w", err)
	}

	cfg, ok, parseErr := readScopeConfigFromStoredRecord(record, normalizedScope)
	if parseErr != nil {
		logging.Warn("Failed to parse stored scoped MCP config; returning empty", "project_id", projectID, "scope", normalizedScope.String(), "error", parseErr)
		return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
	}
	if ok {
		return cfg, nil
	}

	return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
}

func (s *MCPService) readMergedScopedServers(ctx context.Context, projectID string) (map[string]serverScopeConfig, error) {
	merged := make(map[string]serverScopeConfig)
	for _, scope := range []reliantv1.ConfigScope{
		reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL,
		reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
		reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL,
	} {
		cfg, err := s.readMCPConfigForScope(ctx, projectID, scope)
		if err != nil {
			return nil, fmt.Errorf("failed to read MCP configuration for %s scope: %w", scope.String(), err)
		}

		for name, serverCfg := range cfg.MCPServers {
			merged[name] = serverScopeConfig{Config: serverCfg, Scope: scope}
		}
	}

	return merged, nil
}

// projectBelongsToUser checks if a project belongs to the authenticated user.
func (s *MCPService) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("database error"))
	}
	return nil
}

func (s *MCPService) getProjectPath(ctx context.Context, projectID string, userID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", fmt.Errorf("project_id is required")
	}
	if s == nil || s.database == nil {
		return "", fmt.Errorf("database not available")
	}

	project, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return "", fmt.Errorf("project not found")
	}
	if project == nil || strings.TrimSpace(project.Path) == "" {
		return "", fmt.Errorf("project not found")
	}

	return project.Path, nil
}

func (s *MCPService) removeServerFromAllScopes(ctx context.Context, projectID, userID, name string) (map[reliantv1.ConfigScope]*MCPConfigFile, error) {
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}

	updatedScopes := make(map[reliantv1.ConfigScope]*MCPConfigFile)

	for _, scope := range []reliantv1.ConfigScope{
		reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL,
		reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
		reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL,
	} {
		cfg, err := s.readMCPConfigForScope(ctx, projectID, scope)
		if err != nil {
			logging.Warn("Failed reading MCP config for uninstall", "scope", scope.String(), "error", err)
			continue
		}
		if _, exists := cfg.MCPServers[name]; !exists {
			continue
		}
		delete(cfg.MCPServers, name)
		if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scope, cfg); err != nil {
			return nil, err
		}
		updatedScopes[scope] = cfg
	}

	return updatedScopes, nil
}

func (s *MCPService) ensureProjectLocalGitignore(ctx context.Context, projectID string) {
	project, err := s.database.GetProject(ctx, projectID)
	if err != nil {
		logging.Warn("Failed to get project while ensuring .reliant.local is gitignored", "project_id", projectID, "error", err)
		return
	}

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || s.daemonRouter == nil {
		logging.Warn("Cannot ensure gitignore: no user context or daemon router")
		return
	}

	payload, _ := json.Marshal(struct {
		ProjectPath string `json:"project_path"`
		Pattern     string `json:"pattern"`
	}{ProjectPath: project.Path, Pattern: ".reliant.local/"})

	if _, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "mcp.ensure_gitignore", payload, 10000); err != nil {
		logging.Warn("Failed to ensure .reliant.local/ is gitignored via daemon", "project_path", project.Path, "error", err)
	}
}

// writeMCPConfigViaDaemon sends a daemon command to write the MCP config file on the daemon's filesystem.
func (s *MCPService) writeMCPConfigViaDaemon(ctx context.Context, projectID, projectPath string, scope reliantv1.ConfigScope, cfg *MCPConfigFile) error {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return fmt.Errorf("no user context for daemon config write")
	}
	if s.daemonRouter == nil {
		return fmt.Errorf("daemon router not available for config write")
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	payload, err := json.Marshal(struct {
		Scope       string `json:"scope"`
		ProjectPath string `json:"project_path"`
		Content     string `json:"content"`
	}{
		Scope:       scopeToDaemonWriteScope(scope),
		ProjectPath: projectPath,
		Content:     string(data),
	})
	if err != nil {
		return fmt.Errorf("failed to marshal daemon write request: %w", err)
	}

	respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "mcp.write_config", payload, 15000)
	if err != nil {
		return fmt.Errorf("daemon config write failed: %w", err)
	}

	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("failed to parse daemon write response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("daemon config write failed: %s", resp.Error)
	}

	return nil
}

// convertToMCPServerConfig converts internal config.MCPServer to file format
func convertToMCPServerConfig(server config.MCPServer) MCPServerConfig {
	serverCfg := MCPServerConfig{
		Command: server.Command,
		Args:    server.Args,
		Type:    string(server.Type),
		URL:     server.URL,
		Headers: server.Headers,
		Enabled: ptr.Of(server.Enabled),
	}

	// Convert env from []string to map[string]string
	if len(server.Env) > 0 {
		serverCfg.Env = make(map[string]string)
		for _, envVar := range server.Env {
			parts := strings.SplitN(envVar, "=", 2)
			if len(parts) == 2 {
				serverCfg.Env[parts[0]] = parts[1]
			}
		}
	}

	return serverCfg
}

// protoConfigToInternal converts proto MCPServerConfig to internal config.MCPServer
func protoConfigToInternal(cfg *reliantv1.MCPServerConfig) config.MCPServer {
	serverType := config.MCPStdio
	if cfg.Type != "" {
		serverType = config.MCPType(cfg.Type)
	}

	envMap := make(map[string]string)
	for _, envVar := range cfg.Env {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	expandWithEnv := func(input string) string {
		return os.Expand(input, func(key string) string {
			if value, ok := envMap[key]; ok {
				return value
			}
			return os.Getenv(key)
		})
	}

	server := config.MCPServer{
		Command: expandWithEnv(cfg.Command),
		Args:    make([]string, 0, len(cfg.Args)),
		Env:     cfg.Env,
		Type:    serverType,
		Headers: make(map[string]string, len(cfg.Headers)),
		Enabled: true,
	}
	for _, arg := range cfg.Args {
		server.Args = append(server.Args, expandWithEnv(arg))
	}
	for k, v := range cfg.Headers {
		server.Headers[k] = expandWithEnv(v)
	}
	if cfg.Url != nil {
		server.URL = expandWithEnv(*cfg.Url)
	}
	return server
}

func scopedConfigToInternal(cfg MCPServerConfig) config.MCPServer {
	serverType := config.MCPStdio
	if cfg.Type != "" {
		serverType = config.MCPType(cfg.Type)
	}

	envMap := make(map[string]string)
	for k, v := range cfg.Env {
		envMap[k] = v
	}

	expandWithEnv := func(input string) string {
		return os.Expand(input, func(key string) string {
			if value, ok := envMap[key]; ok {
				return value
			}
			return os.Getenv(key)
		})
	}

	server := config.MCPServer{
		Command: expandWithEnv(cfg.Command),
		Args:    make([]string, 0, len(cfg.Args)),
		Type:    serverType,
		URL:     expandWithEnv(cfg.URL),
		Headers: make(map[string]string, len(cfg.Headers)),
		Enabled: cfg.IsEnabled(),
	}
	for _, arg := range cfg.Args {
		server.Args = append(server.Args, expandWithEnv(arg))
	}
	for k, v := range cfg.Env {
		server.Env = append(server.Env, k+"="+v)
	}
	for k, v := range cfg.Headers {
		server.Headers[k] = expandWithEnv(v)
	}

	return server
}

func normalizeScope(scope reliantv1.ConfigScope) reliantv1.ConfigScope {
	if scope == reliantv1.ConfigScope_CONFIG_SCOPE_UNSPECIFIED {
		return reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	}
	return scope
}

// ============================================================================
// RPC Methods
// ============================================================================

// ListServers returns all configured MCP servers with their status
func (s *MCPService) ListServers(
	ctx context.Context,
	req *connect.Request[reliantv1.ListServersRequest],
) (*connect.Response[reliantv1.ListServersResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Read merged scoped config to get all configured servers
	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	// Check if MCP manager is available
	if s.daemonRouter == nil {
		return connect.NewResponse(&reliantv1.ListServersResponse{
			Servers: []*reliantv1.MCPServer{},
			Total:   int32(len(scopedServers)),
		}), nil
	}

	// Get all clients from runtime
	mgr := s.mcpManagerForUser(userID)
	clients := mgr.GetProjectClients(projectPath)
	healthStatus := mgr.GetProjectHealthStatus(projectPath)

	// Build server list from merged scoped config (source of truth)
	servers := make([]*reliantv1.MCPServer, 0, len(scopedServers))
	names := make([]string, 0, len(scopedServers))
	for name := range scopedServers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		scoped := scopedServers[name]
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
			Enabled: scoped.Config.IsEnabled(),
			Scope:   scoped.Scope,
		}
		if cfg.URL != "" {
			server.Config.Url = &cfg.URL
		}

		// Convert env from map to []string
		if len(cfg.Env) > 0 {
			for k, v := range cfg.Env {
				server.Config.Env = append(server.Config.Env, k+"="+v)
			}
		}

		if !scoped.Config.IsEnabled() {
			server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED
			server.Connected = false
			server.Healthy = false
			servers = append(servers, server)
			continue
		}

		// Check if server is running and get its status
		if client, exists := clients[name]; exists {
			healthy := healthStatus[name]
			connected := client.IsConnected()

			mcpStatus := reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED
			if connected && healthy {
				mcpStatus = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY
			} else if connected {
				mcpStatus = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNHEALTHY
			}

			server.Status = mcpStatus
			server.Connected = connected
			server.Healthy = healthy

			// Get server info
			if info := client.ServerInfo(); info != nil {
				server.ServerInfo = &reliantv1.MCPServerInfo{
					Name:    info.Name,
					Version: info.Version,
				}
				server.ResourcesEnabled = info.Capabilities.Resources != nil
				server.PromptsEnabled = info.Capabilities.Prompts != nil
			}

			// Get tool count
			if tools, err := client.ListTools(); err == nil {
				server.ToolCount = int32(len(tools))
			} else {
				errStr := err.Error()
				server.LastError = &errStr
			}
			if lastErr := mgr.GetProjectLastError(projectPath, name); lastErr != nil {
				errStr := lastErr.Error()
				server.LastError = &errStr
			}
		} else {
			// Server is configured but not running
			server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED
			server.Connected = false
			server.Healthy = false

			// Check if there was an initialization error
			if lastErr := mgr.GetProjectLastError(projectPath, name); lastErr != nil {
				errStr := lastErr.Error()
				server.LastError = &errStr
			}
		}

		servers = append(servers, server)
	}

	return connect.NewResponse(&reliantv1.ListServersResponse{
		Servers: servers,
		Total:   int32(len(servers)),
	}), nil
}

// GetServer returns detailed information about a specific MCP server
func (s *MCPService) GetServer(
	ctx context.Context,
	req *connect.Request[reliantv1.GetServerRequest],
) (*connect.Response[reliantv1.GetServerResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	if s.daemonRouter == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("MCP not available"))
	}

	mgr := s.mcpManagerForUser(userID)

	scoped, configured := scopedServers[name]
	if !configured {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	cfgType := scoped.Config.Type
	if cfgType == "" {
		cfgType = string(config.MCPStdio)
	}
	server := &reliantv1.MCPServer{
		Name: name,
		Config: &reliantv1.MCPServerConfig{
			Command: scoped.Config.Command,
			Args:    scoped.Config.Args,
			Headers: scoped.Config.Headers,
			Type:    cfgType,
		},
		Enabled: scoped.Config.IsEnabled(),
		Scope:   scoped.Scope,
	}
	if scoped.Config.URL != "" {
		server.Config.Url = &scoped.Config.URL
	}
	for k, v := range scoped.Config.Env {
		server.Config.Env = append(server.Config.Env, k+"="+v)
	}

	if !scoped.Config.IsEnabled() {
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED
		server.Connected = false
		server.Healthy = false
		if lastErr := mgr.GetProjectLastError(projectPath, name); lastErr != nil {
			errStr := lastErr.Error()
			server.LastError = &errStr
		}
		return connect.NewResponse(&reliantv1.GetServerResponse{Server: server}), nil
	}

	client, exists := mgr.GetProjectClient(projectPath, name)
	healthStatus := mgr.GetProjectHealthStatus(projectPath)
	if exists {
		healthy := healthStatus[name]
		connected := client.IsConnected()

		mcpStatus := reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED
		if connected && healthy {
			mcpStatus = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY
		} else if connected {
			mcpStatus = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNHEALTHY
		}

		server.Status = mcpStatus
		server.Connected = connected
		server.Healthy = healthy

		if info := client.ServerInfo(); info != nil {
			server.ServerInfo = &reliantv1.MCPServerInfo{
				Name:    info.Name,
				Version: info.Version,
			}
			server.ResourcesEnabled = info.Capabilities.Resources != nil
			server.PromptsEnabled = info.Capabilities.Prompts != nil
		}

		if tools, err := client.ListTools(); err == nil {
			server.ToolCount = int32(len(tools))
		} else {
			errStr := err.Error()
			server.LastError = &errStr
		}
	} else {
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED
		server.Connected = false
		server.Healthy = false
	}

	if lastErr := mgr.GetProjectLastError(projectPath, name); lastErr != nil {
		errStr := lastErr.Error()
		server.LastError = &errStr
	}

	return connect.NewResponse(&reliantv1.GetServerResponse{Server: server}), nil
}

// GetServerTools returns the list of tools exposed by a specific MCP server.
func (s *MCPService) GetServerTools(
	ctx context.Context,
	req *connect.Request[reliantv1.GetServerToolsRequest],
) (*connect.Response[reliantv1.GetServerToolsResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	scoped, configured := scopedServers[name]
	if !configured {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	if !scoped.Config.IsEnabled() {
		return connect.NewResponse(&reliantv1.GetServerToolsResponse{Tools: []*reliantv1.MCPTool{}, Total: 0}), nil
	}

	if s.daemonRouter == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("MCP not available"))
	}

	client, exists := s.mcpManagerForUser(userID).GetProjectClient(projectPath, name)
	if !exists {
		return connect.NewResponse(&reliantv1.GetServerToolsResponse{Tools: []*reliantv1.MCPTool{}, Total: 0}), nil
	}

	tools, err := client.ListTools()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list tools: %w", err))
	}

	respTools := make([]*reliantv1.MCPTool, 0, len(tools))
	for _, tool := range tools {
		respTools = append(respTools, &reliantv1.MCPTool{
			Name:        tool.Name,
			Description: tool.Description,
		})
	}

	sort.Slice(respTools, func(i, j int) bool {
		return respTools[i].Name < respTools[j].Name
	})

	return connect.NewResponse(&reliantv1.GetServerToolsResponse{
		Tools: respTools,
		Total: int32(len(respTools)),
	}), nil
}

// InstallServer installs a new MCP server
func (s *MCPService) InstallServer(
	ctx context.Context,
	req *connect.Request[reliantv1.InstallServerRequest],
) (*connect.Response[reliantv1.InstallServerResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		logging.Error("InstallServer: project_id is required")
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	name := req.Msg.Name
	if name == "" {
		logging.Error("InstallServer: server name is required")
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	scope := req.Msg.Scope
	logging.Info("InstallServer request received", "project_id", projectID, "name", name, "scope", scope.String())

	if s.daemonRouter == nil {
		logging.Error("InstallServer: MCP manager is nil")
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("MCP not available - manager not initialized"))
	}

	// Read MCP config for the requested scope
	mcpConfig, err := s.readMCPConfigForScope(ctx, projectID, scope)
	if err != nil {
		logging.Error("InstallServer: failed to read MCP config", "error", err, "scope", scope.String())
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	// Convert proto config to internal format
	internalConfig := protoConfigToInternal(req.Msg.Config)

	// Load recommended catalog entry for optional bundled skills installation.
	var recommendedEntry *config.RecommendedMCP
	if recommendedConfig, cfgErr := config.LoadRecommendedMCPs(); cfgErr == nil {
		for i := range recommendedConfig.Recommended {
			if recommendedConfig.Recommended[i].Name == name {
				recommendedEntry = &recommendedConfig.Recommended[i]
				break
			}
		}
	}

	// Docker host env detection is handled daemon-side during tool execution.
	// No server-side DOCKER_HOST probing needed.

	if err := s.preflightMCPServer(ctx, name, internalConfig); err != nil {
		logging.Error("MCP server install failed",
			"operation", "install",
			"server", name,
			"phase", "preflight",
			"error", err,
		)
		return connect.NewResponse(&reliantv1.InstallServerResponse{
			Success: false,
			Message: fmt.Sprintf("Preflight failed: %v", err),
			Name:    name,
		}), nil
	}

	// Add server to config file
	previousCfg, hadPreviousCfg := mcpConfig.MCPServers[name]
	mcpConfig.MCPServers[name] = convertToMCPServerConfig(internalConfig)

	// Write updated config via daemon
	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scope, mcpConfig); err != nil {
		logging.Error("Failed to write MCP config", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))
	}

	if scope == reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL {
		s.ensureProjectLocalGitignore(ctx, projectID)
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		normalizeScope(scope): mcpConfig,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	bundleSummary, bundleErr := s.installBundledSkillsForRecommended(ctx, projectPath, scope, recommendedEntry)
	if bundleErr != nil {
		// Roll back MCP config update to avoid partial silent state.
		if hadPreviousCfg {
			mcpConfig.MCPServers[name] = previousCfg
		} else {
			delete(mcpConfig.MCPServers, name)
		}
		if rollbackErr := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scope, mcpConfig); rollbackErr != nil {
			logging.Error("Failed to rollback MCP config after skill bundle failure", "error", rollbackErr, "server", name)
		}
		if persistErr := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
			normalizeScope(scope): mcpConfig,
		}); persistErr != nil {
			logging.Error("Failed to rollback stored MCP scope snapshot after skill bundle failure", "error", persistErr, "server", name)
		}

		logging.Error("MCP server install failed",
			"operation", "install",
			"server", name,
			"phase", "skill_bundle",
			"error", bundleErr,
		)
		return connect.NewResponse(&reliantv1.InstallServerResponse{
			Success: false,
			Message: fmt.Sprintf("Skill bundle installation failed; MCP install rolled back. Error: %v", bundleErr),
			Name:    name,
		}), nil
	}

	// Add the server to the running manager
	startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
	defer cancel()

	if internalConfig.Enabled {
		if err := s.mcpManagerForUser(userID).AddProjectServer(startCtx, projectPath, name, internalConfig); err != nil {
			logging.Error("MCP server install failed",
				"operation", "install",
				"server", name,
				"phase", "startup",
				"error", err,
			)
			return connect.NewResponse(&reliantv1.InstallServerResponse{
				Success: false,
				Message: fmt.Sprintf("Server configuration saved, but failed to start. Fix configuration/runtime and retry restart. Error: %v", err),
				Name:    name,
			}), nil
		}
	}

	logging.Info("MCP server installed successfully", "name", name, "scope", scope.String())

	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
			logging.Warn("Failed to emit config_health refetch after InstallServer", "error", err)
		}
	}

	message := "Server installed successfully"
	if strings.TrimSpace(bundleSummary) != "" {
		message = message + ". " + bundleSummary
	}
	return connect.NewResponse(&reliantv1.InstallServerResponse{
		Success: true,
		Message: message,
		Name:    name,
	}), nil
}

func installSkillBundleTargetDir(projectPath string, scope reliantv1.ConfigScope, targetScope string) (string, error) {
	scopeName := strings.TrimSpace(strings.ToLower(targetScope))
	switch scopeName {
	case "", "inherit":
		switch normalizeScope(scope) {
		case reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL:
			return filepath.Join(config.GetUserConfigDir(), "skills"), nil
		case reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL:
			return filepath.Join(projectPath, config.ReliantLocalDir, "skills"), nil
		default:
			return filepath.Join(projectPath, config.ReliantDir, "skills"), nil
		}
	case "project":
		return filepath.Join(projectPath, config.ReliantDir, "skills"), nil
	case "project_local", "local":
		return filepath.Join(projectPath, config.ReliantLocalDir, "skills"), nil
	case "global", "user":
		return filepath.Join(config.GetUserConfigDir(), "skills"), nil
	default:
		return "", fmt.Errorf("unsupported skill bundle target scope: %s", targetScope)
	}
}

func (s *MCPService) installBundledSkillsForRecommended(ctx context.Context, projectPath string, scope reliantv1.ConfigScope, rec *config.RecommendedMCP) (string, error) {
	if rec == nil || rec.Bundles == nil || len(rec.Bundles.Skills) == 0 {
		return "", nil
	}

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || s.daemonRouter == nil {
		return "", fmt.Errorf("cannot install bundles: no user context or daemon router")
	}

	totalInstalled := 0
	totalSkipped := 0
	for _, bundle := range rec.Bundles.Skills {
		targetDir, err := installSkillBundleTargetDir(projectPath, scope, bundle.TargetScope)
		if err != nil {
			return "", err
		}

		type bundleFile struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		files := make([]bundleFile, 0, len(bundle.Files))
		for _, f := range bundle.Files {
			files = append(files, bundleFile{Path: f.Path, Content: f.Content})
		}

		payload, err := json.Marshal(struct {
			TargetDir      string       `json:"target_dir"`
			Files          []bundleFile `json:"files"`
			ConflictPolicy string       `json:"conflict_policy"`
		}{
			TargetDir:      targetDir,
			Files:          files,
			ConflictPolicy: bundle.ConflictPolicy,
		})
		if err != nil {
			return "", fmt.Errorf("failed to marshal bundle install request: %w", err)
		}

		respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "mcp.install_bundle", payload, 30000)
		if err != nil {
			return "", fmt.Errorf("daemon bundle install failed: %w", err)
		}

		var resp struct {
			Installed int    `json:"installed"`
			Skipped   int    `json:"skipped"`
			Error     string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return "", fmt.Errorf("failed to parse bundle install response: %w", err)
		}
		if resp.Error != "" {
			return "", fmt.Errorf("daemon bundle install failed: %s", resp.Error)
		}

		totalInstalled += resp.Installed
		totalSkipped += resp.Skipped
	}

	if totalInstalled == 0 && totalSkipped == 0 {
		return "", nil
	}
	if totalSkipped > 0 {
		return fmt.Sprintf("Installed %d bundled skill file(s), skipped %d due to conflict policy", totalInstalled, totalSkipped), nil
	}
	return fmt.Sprintf("Installed %d bundled skill file(s)", totalInstalled), nil
}

// checkResourceExistsViaDaemon checks whether a file/path exists on the daemon's filesystem.
func (s *MCPService) checkResourceExistsViaDaemon(ctx context.Context, path string) bool {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || s.daemonRouter == nil {
		return false
	}

	payload, _ := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})

	respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "mcp.check_resource", payload, 5000)
	if err != nil {
		return false
	}

	var resp struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return false
	}
	return resp.Exists
}

func (s *MCPService) preflightMCPServer(ctx context.Context, name string, cfg config.MCPServer) error {
	name = strings.ToLower(strings.TrimSpace(name))

	if cfg.Type == "" {
		cfg.Type = config.MCPStdio
	}

	switch cfg.Type {
	case config.MCPStdio:
		if strings.TrimSpace(cfg.Command) == "" {
			return fmt.Errorf("stdio server requires a command")
		}
		finder := cmdutil.NewCommandFinder(cfg.Command)
		if _, err := finder.Find(); err != nil {
			return fmt.Errorf("command %q not found on PATH", cfg.Command)
		}
	case config.MCPSse, config.MCPHTTP:
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("http/sse server requires a URL")
		}
		u, err := url.Parse(cfg.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid sse URL %q (must start with http:// or https://)", cfg.URL)
		}
	default:
		return fmt.Errorf("unsupported MCP transport type: %s", cfg.Type)
	}

	envMap := make(map[string]string, len(cfg.Env))
	for _, envVar := range cfg.Env {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envMap[parts[0]] = parts[1]
	}

	requiredByServer := map[string][]string{
		"context7":   {"CONTEXT7_API_KEY"},
		"supabase":   {"SUPABASE_ACCESS_TOKEN"},
		"github":     {"GITHUB_PERSONAL_ACCESS_TOKEN"},
		"postgres":   {"POSTGRES_CONNECTION_STRING"},
		"sqlite":     {"SQLITE_DB_PATH"},
		"filesystem": {"ALLOWED_DIRECTORIES"},
		"slack":      {"SLACK_BOT_TOKEN", "SLACK_TEAM_ID"},
		"sentry":     {"SENTRY_ACCESS_TOKEN"},
		"linear":     {"LINEAR_API_KEY"},
	}

	if required, ok := requiredByServer[name]; ok {
		for _, key := range required {
			if strings.TrimSpace(envMap[key]) == "" {
				return fmt.Errorf("missing required configuration value: %s", key)
			}
		}
	}

	if name == "zen" {
		hasAnyProviderKey := strings.TrimSpace(envMap["OPENAI_API_KEY"]) != "" ||
			strings.TrimSpace(envMap["GEMINI_API_KEY"]) != "" ||
			strings.TrimSpace(envMap["OPENROUTER_API_KEY"]) != ""
		if !hasAnyProviderKey {
			return fmt.Errorf("zen requires at least one provider key (OPENAI_API_KEY, GEMINI_API_KEY, or OPENROUTER_API_KEY)")
		}
	}

	if name == "postgres" {
		conn := strings.TrimSpace(envMap["POSTGRES_CONNECTION_STRING"])
		if conn != "" && !strings.HasPrefix(conn, "postgresql://") && !strings.HasPrefix(conn, "postgres://") {
			return fmt.Errorf("POSTGRES_CONNECTION_STRING must start with postgres:// or postgresql://")
		}
	}

	if name == "sqlite" {
		dbPath := strings.TrimSpace(envMap["SQLITE_DB_PATH"])
		if dbPath == "" {
			return fmt.Errorf("missing required configuration value: SQLITE_DB_PATH")
		}
		if !s.checkResourceExistsViaDaemon(ctx, dbPath) {
			return fmt.Errorf("SQLITE_DB_PATH does not exist: %s", dbPath)
		}
	}

	if name == "docker" {
		host := strings.TrimSpace(envMap["DOCKER_HOST"])
		if strings.HasPrefix(host, "unix://") {
			socketPath := strings.TrimPrefix(host, "unix://")
			if !s.checkResourceExistsViaDaemon(ctx, socketPath) {
				return fmt.Errorf("docker socket not found at %s", socketPath)
			}
		}
	}

	return nil
}

// UninstallServer removes an MCP server
func (s *MCPService) UninstallServer(
	ctx context.Context,
	req *connect.Request[reliantv1.UninstallServerRequest],
) (*connect.Response[reliantv1.UninstallServerResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	if s.daemonRouter == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("MCP not available"))
	}

	// Remove from running manager first
	if err := s.mcpManagerForUser(userID).RemoveProjectServer(projectPath, name); err != nil {
		logging.Warn("Failed to remove MCP server from manager during uninstall", "error", err, "name", name)
		// Continue anyway to remove from config files
	}

	updatedScopes, err := s.removeServerFromAllScopes(ctx, projectID, userID, name)
	if err != nil {
		logging.Error("Failed to remove MCP server from scoped configs", "error", err, "name", name)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, updatedScopes); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	logging.Info("MCP server uninstalled successfully", "name", name)

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UninstallServer", "error", err)
	}

	return connect.NewResponse(&reliantv1.UninstallServerResponse{
		Success: true,
		Message: "Server uninstalled successfully",
		Name:    name,
	}), nil
}

// RestartServer restarts an MCP server
func (s *MCPService) RestartServer(
	ctx context.Context,
	req *connect.Request[reliantv1.RestartServerRequest],
) (*connect.Response[reliantv1.RestartServerResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	if s.daemonRouter == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("MCP not available"))
	}

	mgr := s.mcpManagerForUser(userID)

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	scoped, found := scopedServers[name]
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found in configuration"))
	}

	if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
		logging.Warn("RestartServer: failed to remove existing server before restart", "name", name, "error", err)
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
		logging.Error("MCP server restart failed",
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
		logging.Error("MCP server restart failed",
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
		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
			logging.Warn("Failed to emit config_health refetch after RestartServer", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.RestartServerResponse{
		Success: true,
		Message: "Server restarted successfully",
		Name:    name,
	}), nil
}

// ListRecommended returns a list of recommended MCP servers
func (s *MCPService) ListRecommended(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRecommendedRequest],
) (*connect.Response[reliantv1.ListRecommendedResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	if err := s.projectBelongsToUser(ctx, projectID, userID); err != nil {
		return nil, err
	}

	// Load recommended MCPs from YAML config
	recommendedConfig, err := config.LoadRecommendedMCPs()
	if err != nil {
		logging.Error("Failed to load recommended MCPs", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load recommended MCPs: %w", err))
	}

	// Get currently installed servers from merged scoped configs (source of truth)
	installedServers := make(map[string]bool)
	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err == nil {
		for name := range scopedServers {
			installedServers[name] = true
		}
	}

	// Build response from YAML config
	recommended := make([]*reliantv1.RecommendedServer, 0, len(recommendedConfig.Recommended))
	for _, rec := range recommendedConfig.Recommended {
		// Convert config fields
		configFields := make([]*reliantv1.ConfigField, 0, len(rec.ConfigFields))
		for _, field := range rec.ConfigFields {
			cf := &reliantv1.ConfigField{
				Key:      field.Key,
				Label:    field.Label,
				Type:     field.Type,
				Required: field.Required,
			}
			if field.Placeholder != "" {
				cf.Placeholder = &field.Placeholder
			}
			if field.HelpText != "" {
				cf.HelpText = &field.HelpText
			}
			if field.ValidationRegex != "" {
				cf.ValidationRegex = &field.ValidationRegex
			}
			if field.ValidationMessage != "" {
				cf.ValidationMessage = &field.ValidationMessage
			}
			configFields = append(configFields, cf)
		}

		// Convert server config
		serverCfg := rec.Config.ToMCPServer()
		protoConfig := &reliantv1.MCPServerConfig{
			Command: serverCfg.Command,
			Args:    serverCfg.Args,
			Env:     serverCfg.Env,
			Headers: serverCfg.Headers,
			Type:    string(serverCfg.Type),
		}
		if serverCfg.URL != "" {
			protoConfig.Url = &serverCfg.URL
		}

		server := &reliantv1.RecommendedServer{
			Name:         rec.Name,
			DisplayName:  rec.DisplayName,
			Description:  rec.Description,
			Category:     rec.Category,
			ConfigFields: configFields,
			Config:       protoConfig,
			Installed:    installedServers[rec.Name],
		}
		if rec.Bundles != nil && len(rec.Bundles.Skills) > 0 {
			skillNames := make([]string, 0)
			for _, bundle := range rec.Bundles.Skills {
				if strings.TrimSpace(bundle.Name) != "" {
					skillNames = append(skillNames, bundle.Name)
				}
			}
			if len(skillNames) > 0 {
				msg := fmt.Sprintf("Includes %d bundled skill(s): %s", len(skillNames), strings.Join(skillNames, ", "))
				server.Description = strings.TrimSpace(server.Description + "\n\n" + msg)
			}
		}

		if rec.SetupRequired {
			server.SetupRequired = &rec.SetupRequired
		}
		if rec.SetupInstructions != "" {
			server.SetupInstructions = &rec.SetupInstructions
		}
		if rec.ConfigTemplate != "" {
			server.ConfigTemplate = &rec.ConfigTemplate
		}
		if rec.DocsURL != "" {
			server.DocsUrl = &rec.DocsURL
		}

		recommended = append(recommended, server)
	}

	return connect.NewResponse(&reliantv1.ListRecommendedResponse{
		Recommended: recommended,
		Total:       int32(len(recommended)),
	}), nil
}

// UpdateServerConfig updates the configuration of an MCP server
func (s *MCPService) UpdateServerConfig(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateServerConfigRequest],
) (*connect.Response[reliantv1.UpdateServerConfigResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	logging.Info("UpdateServerConfig request", "project_id", projectID, "name", name, "env_keys", len(req.Msg.Env))

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	scoped, exists := scopedServers[name]
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	mcpConfig, err := s.readMCPConfigForScope(ctx, projectID, scoped.Scope)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
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
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		scoped.Scope: mcpConfig,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if s.daemonRouter != nil {
		mgr := s.mcpManagerForUser(userID)
		if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
			logging.Warn("Failed to remove server before restart", "error", err)
		}

		intCfg := scopedConfigToInternal(serverCfg)
		if intCfg.Enabled {
			if err := mgr.AddProjectServer(ctx, projectPath, name, intCfg); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("configuration saved to %s scope but failed to restart server: %w", scoped.Scope.String(), err))
			}
		}
	}

	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
			logging.Warn("Failed to emit config_health refetch after UpdateServerConfig", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.UpdateServerConfigResponse{Success: true, Message: "Configuration updated successfully"}), nil
}

// SetServerEnabled toggles whether a server is enabled without uninstalling.
func (s *MCPService) SetServerEnabled(
	ctx context.Context,
	req *connect.Request[reliantv1.SetServerEnabledRequest],
) (*connect.Response[reliantv1.SetServerEnabledResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	scoped, exists := scopedServers[name]
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	mcpConfig, err := s.readMCPConfigForScope(ctx, projectID, scoped.Scope)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	serverCfg, existsInTargetScope := mcpConfig.MCPServers[name]
	if !existsInTargetScope {
		serverCfg = scoped.Config
	}
	serverCfg.Enabled = ptr.Of(req.Msg.Enabled)
	mcpConfig.MCPServers[name] = serverCfg

	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scoped.Scope, mcpConfig); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		scoped.Scope: mcpConfig,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if s.daemonRouter != nil {
		mgr := s.mcpManagerForUser(userID)
		if req.Msg.Enabled {
			if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
				logging.Warn("Failed to remove server before enable restart", "name", name, "error", err)
			}

			intCfg := scopedConfigToInternal(serverCfg)
			startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
			defer cancel()

			if err := s.preflightMCPServer(ctx, name, intCfg); err != nil {
				logging.Error("MCP server enable/disable failed",
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
				logging.Error("MCP server enable/disable failed",
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
				logging.Warn("Failed to stop server while disabling", "name", name, "error", err)
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
func (s *MCPService) MoveServerScope(
	ctx context.Context,
	req *connect.Request[reliantv1.MoveServerScopeRequest],
) (*connect.Response[reliantv1.MoveServerScopeResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}

	userID := auth.MustGetUserID(ctx)
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	targetScope := normalizeScope(req.Msg.Scope)

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))
	}

	scoped, exists := scopedServers[name]
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	sourceScope := normalizeScope(scoped.Scope)

	if scoped.Scope == targetScope {
		return connect.NewResponse(&reliantv1.MoveServerScopeResponse{
			Success: true,
			Message: "Server is already stored in the requested scope",
			Name:    name,
			Scope:   targetScope,
		}), nil
	}

	sourceConfig, err := s.readMCPConfigForScope(ctx, projectID, sourceScope)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read source MCP configuration: %w", err))
	}

	targetConfig, err := s.readMCPConfigForScope(ctx, projectID, targetScope)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read target MCP configuration: %w", err))
	}

	serverCfg, sourceExists := sourceConfig.MCPServers[name]
	if !sourceExists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found in source scope"))
	}

	targetConfig.MCPServers[name] = serverCfg
	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, targetScope, targetConfig); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write target MCP configuration: %w", err))
	}

	delete(sourceConfig.MCPServers, name)
	if err := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, sourceScope, sourceConfig); err != nil {
		// Best effort rollback: restore source entry if source write failed after target write.
		sourceConfig.MCPServers[name] = serverCfg
		_ = s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, sourceScope, sourceConfig)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to finalize move from %s to %s: %w", scoped.Scope.String(), targetScope.String(), err))
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		sourceScope: sourceConfig,
		targetScope: targetConfig,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if targetScope == reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL {
		s.ensureProjectLocalGitignore(ctx, projectID)
	}

	if s.daemonRouter != nil {
		mgr := s.mcpManagerForUser(userID)
		if err := mgr.RemoveProjectServer(projectPath, name); err != nil {
			logging.Warn("Failed to remove server before restarting after scope move", "name", name, "error", err)
		}

		intCfg := scopedConfigToInternal(serverCfg)
		if intCfg.Enabled {
			startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
			defer cancel()

			if err := mgr.AddProjectServer(startCtx, projectPath, name, intCfg); err != nil {
				logging.Error("MCP server scope move failed",
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
