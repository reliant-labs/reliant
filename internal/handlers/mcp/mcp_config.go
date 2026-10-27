package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// ============================================================================
// Helper Types
// ============================================================================

// MCPConfigFile represents the structure of .mcp.json
type MCPConfigFile struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// MCPServerConfig represents a server config in .mcp.json format.
//
// It mirrors config.MCPServer for the settings round trip, so a field missing
// here is a field a user loses the moment they save through the UI. Dir,
// DirScoped and RequiresFiles are carried for exactly that reason.
type MCPServerConfig struct {
	Command       string            `json:"command"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Type          string            `json:"type,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Enabled       *bool             `json:"enabled,omitempty"`
	Dir           string            `json:"dir,omitempty"`
	DirScoped     bool              `json:"dirScoped,omitempty"`
	RequiresFiles []string          `json:"requiresFiles,omitempty"`
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

func (s *Service) buildUpdatedStoredMCPConfigs(existing *string, scope reliantv1.ConfigScope, updatedScopeCfg *MCPConfigFile) (*string, error) {
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

func (s *Service) persistStoredMCPConfigScopes(
	ctx context.Context,
	projectID string,
	updatedScopes map[reliantv1.ConfigScope]*MCPConfigFile,
) error {
	if len(updatedScopes) == 0 {
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

	record, err := s.deps.Repo.GetProjectConfigRecord(ctx, projectID)
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
	if err := s.deps.Repo.UpsertProjectConfigRecord(ctx, record); err != nil {
		return fmt.Errorf("failed to persist stored MCP configuration snapshot: %w", err)
	}

	return nil
}

func (s *Service) persistGlobalScopeSnapshotAcrossProjects(
	ctx context.Context,
	sourceProjectID string,
	globalCfg *MCPConfigFile,
) error {
	if globalCfg == nil {
		return nil
	}

	sourceProject, err := s.deps.Repo.GetProject(ctx, sourceProjectID)
	if err != nil {
		return fmt.Errorf("failed to resolve source project for global MCP snapshot propagation: %w", err)
	}
	if sourceProject == nil || strings.TrimSpace(sourceProject.UserID) == "" {
		return nil
	}

	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{
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

		record, recErr := s.deps.Repo.GetProjectConfigRecord(ctx, project.ID)
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
		if err := s.deps.Repo.UpsertProjectConfigRecord(ctx, record); err != nil {
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
func (s *Service) readMCPConfigForScope(ctx context.Context, projectID string, scope reliantv1.ConfigScope) (*MCPConfigFile, error) {
	normalizedScope := normalizeScope(scope)

	mcpConfigs, err := s.deps.Repo.GetProjectMCPConfigsJSON(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
		}
		return nil, fmt.Errorf("failed to load stored MCP configuration snapshot: %w", err)
	}
	record := &db.ProjectConfigRecord{ProjectID: projectID, MCPConfigs: mcpConfigs}

	cfg, ok, parseErr := readScopeConfigFromStoredRecord(record, normalizedScope)
	if parseErr != nil {
		logging.WarnContext(ctx, "Failed to parse stored scoped MCP config; returning empty", "project_id", projectID, "scope", normalizedScope.String(), "error", parseErr)
		return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
	}
	if ok {
		return cfg, nil
	}

	return &MCPConfigFile{MCPServers: make(map[string]MCPServerConfig)}, nil
}

// readMergedScopedServers is every configured server across the three scopes,
// the narrower scope winning a name. It reads the stored record once: it
// used to read it once per scope, and the record carries every skill body
// the daemon indexed (18 MB in prod), so each MCP page load read it three
// times to use a few hundred bytes of one column.
func (s *Service) readMergedScopedServers(ctx context.Context, projectID string) (map[string]serverScopeConfig, error) {
	mcpConfigs, err := s.deps.Repo.GetProjectMCPConfigsJSON(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]serverScopeConfig{}, nil
		}
		return nil, fmt.Errorf("failed to load stored MCP configuration snapshot: %w", err)
	}
	return readMergedScopedServersFromStoredRecord(&db.ProjectConfigRecord{ProjectID: projectID, MCPConfigs: mcpConfigs}), nil
}

func (s *Service) removeServerFromAllScopes(ctx context.Context, projectID, userID, name string) (map[reliantv1.ConfigScope]*MCPConfigFile, error) {
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
			logging.WarnContext(ctx, "Failed reading MCP config for uninstall", "scope", scope.String(), "error", err)
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

func (s *Service) ensureProjectLocalGitignore(ctx context.Context, projectID string) {
	project, err := s.deps.Repo.GetProject(ctx, projectID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to get project while ensuring .reliant.local is gitignored", "project_id", projectID, "error", err)
		return
	}

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || s.deps.Router == nil {
		logging.WarnContext(ctx, "Cannot ensure gitignore: no user context or daemon router")
		return
	}

	payload, _ := json.Marshal(struct {
		ProjectPath string `json:"project_path"`
		Pattern     string `json:"pattern"`
	}{ProjectPath: project.Path, Pattern: ".reliant.local/"})

	if _, err := s.deps.Router.SendDaemonCommand(ctx, userID, "mcp.ensure_gitignore", payload, 10000); err != nil {
		logging.WarnContext(ctx, "Failed to ensure .reliant.local/ is gitignored via daemon", "project_path", project.Path, "error", err)
	}
}

// writeMCPConfigViaDaemon sends a daemon command to write the MCP config file on the daemon's filesystem.
func (s *Service) writeMCPConfigViaDaemon(ctx context.Context, projectID, projectPath string, scope reliantv1.ConfigScope, cfg *MCPConfigFile) error {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return fmt.Errorf("no user context for daemon config write")
	}
	if s.deps.Router == nil {
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

	// The daemon reloads servers when their config changes.
	defer s.statuses.invalidateUser(userID)
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "mcp.write_config", payload, 15000)
	if err != nil {
		return daemonerr.Error("daemon config write", err)
	}

	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("failed to parse daemon write response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("daemon config write failed: %w", daemonReported(resp.Error))
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

// The two converters below carry a server's command, args, url, headers and
// env exactly as the user wrote them, ${VAR} references included. They run on
// the API server, whose environment is Reliant's — database, vault and NATS
// credentials, OAuth client secrets — and never the user's, so resolving a
// reference here would substitute a server secret into a config that is then
// stored, returned by ListServers and written to the user's machine. The
// daemon resolves references when it launches the server, from the user's own
// environment (mcp.resolveLaunchConfig).

// protoConfigToInternal converts proto MCPServerConfig to internal config.MCPServer
func protoConfigToInternal(cfg *reliantv1.MCPServerConfig) config.MCPServer {
	serverType := config.MCPStdio
	if cfg.Type != "" {
		serverType = config.MCPType(cfg.Type)
	}

	// NOTE: this converter's source is the PROTO MCPServerConfig, which has no
	// dir/dirScoped/requiresFiles fields. Adding them there is a schema change
	// (proto + regeneration) and is deliberately not done here; the file-backed
	// and stored-JSON paths (configloader.LoadMCPServersFromFile,
	// config.parseMCPServersFromJSON) do carry them.
	server := config.MCPServer{
		Command: cfg.Command,
		Args:    append([]string(nil), cfg.Args...),
		Env:     cfg.Env,
		Type:    serverType,
		Headers: make(map[string]string, len(cfg.Headers)),
		Enabled: true,
	}
	for k, v := range cfg.Headers {
		server.Headers[k] = v
	}
	if cfg.Url != nil {
		server.URL = *cfg.Url
	}
	return server
}

func scopedConfigToInternal(cfg MCPServerConfig) config.MCPServer {
	serverType := config.MCPStdio
	if cfg.Type != "" {
		serverType = config.MCPType(cfg.Type)
	}

	server := config.MCPServer{
		Command:       cfg.Command,
		Args:          append([]string(nil), cfg.Args...),
		Type:          serverType,
		URL:           cfg.URL,
		Headers:       make(map[string]string, len(cfg.Headers)),
		Enabled:       cfg.IsEnabled(),
		Dir:           cfg.Dir,
		DirScoped:     cfg.DirScoped,
		RequiresFiles: cfg.RequiresFiles,
	}
	for k, v := range cfg.Env {
		server.Env = append(server.Env, k+"="+v)
	}
	for k, v := range cfg.Headers {
		server.Headers[k] = v
	}

	return server
}
