package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// InstallServer installs a new MCP server
func (s *Service) InstallServer(
	ctx context.Context,
	req *connect.Request[reliantv1.InstallServerRequest],
) (*connect.Response[reliantv1.InstallServerResponse], error) {
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

	scope := req.Msg.Scope
	logging.DebugContext(ctx, "InstallServer request received", "project_id", projectID, "name", name, "scope", scope.String())

	if s.deps.Router == nil {
		return nil, svcerr.Internal("MCP not available - manager not initialized")
	}

	// Read MCP config for the requested scope
	mcpConfig, err := s.readMCPConfigForScope(ctx, projectID, scope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
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
		logging.ErrorContext(ctx, "MCP server install failed",
			"operation", "install",
			"server", name,
			"phase", "preflight",
			"error", err,
		)
		activity.Skip(ctx) // refused before any config was written: nothing was installed
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
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to save MCP configuration", err)))
	}

	if scope == reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL {
		s.ensureProjectLocalGitignore(ctx, projectID)
	}

	if err := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
		normalizeScope(scope): mcpConfig,
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to store MCP configuration snapshot"))
	}

	bundleSummary, bundleErr := s.installBundledSkillsForRecommended(ctx, projectPath, scope, recommendedEntry)
	if bundleErr != nil {
		// Roll back MCP config update to avoid partial silent state.
		if hadPreviousCfg {
			mcpConfig.MCPServers[name] = previousCfg
		} else {
			delete(mcpConfig.MCPServers, name)
		}
		rollbackErr := s.writeMCPConfigViaDaemon(ctx, projectID, projectPath, scope, mcpConfig)
		if rollbackErr != nil {
			logging.ErrorContext(ctx, "Failed to rollback MCP config after skill bundle failure", "error", rollbackErr, "server", name)
		}
		persistErr := s.persistStoredMCPConfigScopes(ctx, projectID, map[reliantv1.ConfigScope]*MCPConfigFile{
			normalizeScope(scope): mcpConfig,
		})
		if persistErr != nil {
			logging.ErrorContext(ctx, "Failed to rollback stored MCP scope snapshot after skill bundle failure", "error", persistErr, "server", name)
		}

		logging.ErrorContext(ctx, "MCP server install failed",
			"operation", "install",
			"server", name,
			"phase", "skill_bundle",
			"error", bundleErr,
		)
		message := "Skill bundle installation failed; MCP install rolled back."
		if rollbackErr == nil && persistErr == nil {
			activity.Skip(ctx) // both the daemon config and stored snapshot were restored
		} else {
			// Keep the declared activity with success=false: configuration may
			// remain changed. An RPC error would also suppress that activity.
			// Do not expose rollback errors, which may contain storage/transport secrets.
			message = "Skill bundle installation failed; MCP configuration rollback could not be confirmed. Configuration may remain changed on the machine or in the stored snapshot. Check the server configuration and retry when the machine is connected."
		}
		return connect.NewResponse(&reliantv1.InstallServerResponse{
			Success: false,
			Message: withDaemonText(message, bundleErr),
			Name:    name,
		}), nil
	}

	// Add the server to the running manager
	startCtx, cancel := context.WithTimeout(ctx, installServerStartupTimeout)
	defer cancel()

	if internalConfig.Enabled {
		if err := s.mcpManagerForUser(userID).AddProjectServer(startCtx, projectPath, name, internalConfig); err != nil {
			logging.ErrorContext(ctx, "MCP server install failed",
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

	logging.InfoContext(ctx, "MCP server installed successfully", "name", name, "scope", scope.String())

	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: &projectID}); err != nil {
			logging.WarnContext(ctx, "Failed to emit config_health refetch after InstallServer", "error", err)
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

func (s *Service) installBundledSkillsForRecommended(ctx context.Context, projectPath string, scope reliantv1.ConfigScope, rec *config.RecommendedMCP) (string, error) {
	if rec == nil || rec.Bundles == nil || len(rec.Bundles.Skills) == 0 {
		return "", nil
	}

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || s.deps.Router == nil {
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

		respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "mcp.install_bundle", payload, 30000)
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
			return "", fmt.Errorf("daemon bundle install failed: %w", daemonReported(resp.Error))
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
func (s *Service) checkResourceExistsViaDaemon(ctx context.Context, path string) bool {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || s.deps.Router == nil {
		return false
	}

	payload, _ := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})

	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "mcp.check_resource", payload, 5000)
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

func (s *Service) preflightMCPServer(ctx context.Context, name string, cfg config.MCPServer) error {
	name = strings.ToLower(strings.TrimSpace(name))

	if cfg.Type == "" {
		cfg.Type = config.MCPStdio
	}

	switch cfg.Type {
	case config.MCPStdio:
		// Whether the command exists is a question about the user's machine,
		// which the daemon answers when it launches the server. This process
		// has neither the user's PATH nor any business running a lookup on a
		// user-supplied command name.
		if strings.TrimSpace(cfg.Command) == "" {
			return fmt.Errorf("stdio server requires a command")
		}
	case config.MCPSse, config.MCPHTTP:
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("http/sse server requires a URL")
		}
		// A URL built from ${VAR} references is only a URL once the daemon
		// resolves them, so only a literal one can be checked here.
		if !strings.Contains(cfg.URL, "$") {
			u, err := url.Parse(cfg.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid sse URL %q (must start with http:// or https://)", cfg.URL)
			}
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
