// Copyright (c) 2025 Reliant Labs

// Package configloader provides filesystem-based configuration loading.
// This package is daemon-only — it reads YAML/JSON config files directly from
// disk and should NOT be imported by server-side code (api-server, temporal-worker).
package configloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"gopkg.in/yaml.v3"
)

// LoaderOptions configures how configs are loaded
type LoaderOptions struct {
	UserConfigDir string // User's home config directory
	Debug         bool
}

// Loader handles configuration loading without global state
type Loader struct {
	opts LoaderOptions
}

// NewLoader creates a new configuration loader
func NewLoader(opts LoaderOptions) (*Loader, error) {
	if opts.UserConfigDir == "" {
		opts.UserConfigDir = config.GetUserConfigDir()
	}

	return &Loader{opts: opts}, nil
}

// LoadForProject loads and merges configs for a specific project
func (l *Loader) LoadForProject(projectPath string) (*config.Config, error) {
	// Load user config from home directory (global scope)
	userConfig, err := l.loadUserConfig()
	if err != nil {
		logging.Warn("Failed to load user config", "error", err)
		// Continue with empty user config
		userConfig = l.newDefaultConfig()
	}

	// Load project config from project directory (project scope)
	projectConfig, err := l.loadProjectConfig(projectPath)
	if err != nil {
		logging.Warn("Failed to load project config", "error", err)
		// Continue with empty project config
		projectConfig = l.newDefaultConfig()
	}

	// Load project-local config from .reliant.local/ (local scope, gitignored)
	localConfig, err := l.loadProjectLocalConfig(projectPath)
	if err != nil {
		logging.Warn("Failed to load project-local config", "error", err)
		// Continue with empty local config
		localConfig = l.newDefaultConfig()
	}

	// Always set the working directory to the project path
	projectConfig.WorkingDir = projectPath
	localConfig.WorkingDir = projectPath

	// Load MCP configs from all three scopes
	// UserConfigDir is already ~/.reliant/, project uses .reliant/ subdirectory
	userMCPConfig := l.loadMCPConfig(l.opts.UserConfigDir)
	projectMCPConfig := l.loadMCPConfig(filepath.Join(projectPath, config.ReliantDir))
	localMCPConfig := l.loadMCPConfig(filepath.Join(projectPath, config.ReliantLocalDir))

	// Merge configs (Global < Project < Local priority)
	merged := l.mergeConfigs(userConfig, projectConfig, localConfig, userMCPConfig, projectMCPConfig, localMCPConfig)

	// Validate the merged config
	if err := l.validateConfig(merged); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return merged, nil
}

// LoadUserConfig loads only the user configuration
func (l *Loader) LoadUserConfig() (*config.Config, error) {
	return l.loadUserConfig()
}

// loadUserConfig loads the user-space configuration from ~/.reliant/config.yaml
func (l *Loader) loadUserConfig() (*config.Config, error) {
	// UserConfigDir is already ~/.reliant/, so just add config.yaml
	configPath := filepath.Join(l.opts.UserConfigDir, config.ConfigFileName+".yaml")
	return l.loadConfigFromFile(configPath, "")
}

// loadProjectConfig loads project-specific configuration from $project/.reliant/config.yaml
func (l *Loader) loadProjectConfig(projectPath string) (*config.Config, error) {
	configPath := filepath.Join(projectPath, config.ReliantDir, config.ConfigFileName+".yaml")
	return l.loadConfigFromFile(configPath, projectPath)
}

// loadProjectLocalConfig loads project-local configuration from .reliant.local/
func (l *Loader) loadProjectLocalConfig(projectPath string) (*config.Config, error) {
	configPath := filepath.Join(projectPath, config.ReliantLocalDir, config.ConfigFileName+".yaml")
	return l.loadConfigFromFile(configPath, projectPath)
}

// loadConfigFromFile loads configuration from a specific YAML file path.
// Returns default config if file doesn't exist.
func (l *Loader) loadConfigFromFile(configPath, workingDir string) (*config.Config, error) {
	cfg := l.newDefaultConfig()
	if workingDir != "" {
		cfg.WorkingDir = workingDir
	}

	// Read the config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Config file doesn't exist - return defaults
			return cfg, nil
		}
		return cfg, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Unmarshal YAML directly into config struct
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Restore working dir (yaml unmarshal may have overwritten it)
	if workingDir != "" {
		cfg.WorkingDir = workingDir
	}

	return cfg, nil
}

// LoadMCPServersFromProject loads MCP server configurations from a project.
// This is a standalone function that can be called without a Loader instance.
// Loads from .reliant/mcp.json only.
// Returns a map of server name to MCPServer config, or nil if no mcp.json exists.
func LoadMCPServersFromProject(projectPath string) map[string]config.MCPServer {
	mcpPath := filepath.Join(projectPath, config.ReliantDir, config.MCPConfigFileName)
	return LoadMCPServersFromFile(mcpPath)
}

// LoadMCPServersFromProjectScopes loads MCP server configurations from all supported scopes
// with precedence: global < project < project-local.
func LoadMCPServersFromProjectScopes(projectPath string) map[string]config.MCPServer {
	merged := make(map[string]config.MCPServer)

	globalPath := filepath.Join(config.GetUserConfigDir(), config.MCPConfigFileName)
	projectPathFile := filepath.Join(projectPath, config.ReliantDir, config.MCPConfigFileName)
	projectLocalPath := filepath.Join(projectPath, config.ReliantLocalDir, config.MCPConfigFileName)

	for _, p := range []string{globalPath, projectPathFile, projectLocalPath} {
		servers := LoadMCPServersFromFile(p)
		for name, cfg := range servers {
			merged[name] = cfg
		}
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}

// LoadMCPServersFromFile loads MCP servers from a specific file path.
// Exported so tests and other daemon-only code can use it directly.
func LoadMCPServersFromFile(mcpConfigPath string) map[string]config.MCPServer {
	// Check if file exists
	if _, err := os.Stat(mcpConfigPath); os.IsNotExist(err) {
		return nil
	}

	// Read the file
	data, err := os.ReadFile(mcpConfigPath)
	if err != nil {
		logging.Warn("Failed to read mcp.json", "error", err, "path", mcpConfigPath)
		return nil
	}

	// Parse the JSON
	var mcpConfig map[string]interface{}
	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		logging.Warn("Failed to parse mcp.json", "error", err, "path", mcpConfigPath)
		return nil
	}

	// Extract mcpServers from Claude format
	mcpServers, ok := mcpConfig["mcpServers"].(map[string]interface{})
	if !ok {
		return nil
	}

	result := make(map[string]config.MCPServer)
	for serverName, serverConfig := range mcpServers {
		serverMap, ok := serverConfig.(map[string]interface{})
		if !ok {
			continue
		}

		mcpServer := config.MCPServer{
			Type:    config.MCPStdio, // Default type
			Enabled: true,
		}

		// Parse env first so command/args/url can reference values defined in this server block
		// (e.g. args: ["--header", "statsig-api-key:${AUTH_TOKEN}"] with env.AUTH_TOKEN).
		serverEnv := make(map[string]string)
		if env, ok := serverMap["env"].(map[string]interface{}); ok {
			mcpServer.Env = make([]string, 0, len(env))
			for key, value := range env {
				resolvedValue := expandEnvVarsLoader(fmt.Sprintf("%v", value))
				serverEnv[key] = resolvedValue
				mcpServer.Env = append(mcpServer.Env, fmt.Sprintf("%s=%s", key, resolvedValue))
			}
		}

		expandWithServerEnv := func(s string) string {
			return expandEnvVarsWithLookup(s, func(key string) (string, bool) {
				if value, ok := serverEnv[key]; ok {
					return value, true
				}
				value := os.Getenv(key)
				if value == "" {
					return "", false
				}
				return value, true
			})
		}

		// Type (optional, defaults to stdio)
		if typ, ok := serverMap["type"].(string); ok && typ != "" {
			mcpServer.Type = config.MCPType(typ)
		}

		// Command (required for stdio, optional for sse/http transports)
		if command, ok := serverMap["command"].(string); ok {
			mcpServer.Command = expandWithServerEnv(command)
		}

		// Args (optional)
		if args, ok := serverMap["args"].([]interface{}); ok {
			mcpServer.Args = make([]string, len(args))
			for i, arg := range args {
				if argStr, ok := arg.(string); ok {
					mcpServer.Args[i] = expandWithServerEnv(argStr)
				}
			}
		}

		// URL (optional, used by sse/http transports)
		if url, ok := serverMap["url"].(string); ok && url != "" {
			mcpServer.URL = expandWithServerEnv(url)
		}

		// Headers (optional, used by sse/http transports)
		if headers, ok := serverMap["headers"].(map[string]interface{}); ok {
			mcpServer.Headers = make(map[string]string, len(headers))
			for key, value := range headers {
				mcpServer.Headers[key] = expandWithServerEnv(fmt.Sprintf("%v", value))
			}
		}

		// Enabled (optional, defaults to true)
		if enabled, ok := serverMap["enabled"].(bool); ok {
			mcpServer.Enabled = enabled
		}

		// Backward-compatible inference: if URL is set and command is empty, treat as HTTP/SSE.
		if mcpServer.Type == config.MCPStdio && mcpServer.Command == "" && mcpServer.URL != "" {
			mcpServer.Type = config.MCPSse
		}

		if mcpServer.Type == config.MCPStdio && mcpServer.Command == "" {
			continue // Skip invalid stdio servers without command
		}
		if mcpServer.Type == config.MCPSse && mcpServer.URL == "" {
			continue // Skip invalid sse servers without URL
		}

		result[serverName] = mcpServer
	}

	return result
}

// loadMCPConfig loads Claude-compatible MCP configuration from a directory
// It checks for mcp.json in the given directory path
func (l *Loader) loadMCPConfig(dirPath string) *config.Config {
	mcpConfigPath := filepath.Join(dirPath, config.MCPConfigFileName)

	// Check if mcp.json exists
	if _, err := os.Stat(mcpConfigPath); os.IsNotExist(err) {
		return nil // No mcp.json file
	}

	// Load servers using the helper function
	servers := LoadMCPServersFromFile(mcpConfigPath)
	if servers == nil {
		return nil
	}

	// Convert to Config format
	cfg := &config.Config{
		MCPServers: servers,
	}

	return cfg
}

// mergeConfigs merges configs from all three scopes with MCP overrides
// Priority: Global < Project < Local (highest priority)
func (l *Loader) mergeConfigs(userConfig, projectConfig, localConfig, userMCPConfig, projectMCPConfig, localMCPConfig *config.Config) *config.Config {
	merged := &config.Config{
		// Project path always from project config
		WorkingDir:  projectConfig.WorkingDir,
		WorktreeDir: userConfig.WorktreeDir, // From user config

		// Merge maps
		MCPServers: make(map[string]config.MCPServer),

		// Debug flags (OR operation across all configs)
		Debug: userConfig.Debug || projectConfig.Debug || localConfig.Debug,

		// Data directory (local > project > user)
		Data: localConfig.Data,
	}

	// Fallback for data directory
	if merged.Data.Directory == "" {
		merged.Data = projectConfig.Data
	}
	if merged.Data.Directory == "" {
		merged.Data = userConfig.Data
	}

	// Merge MCP servers: Global < Project < Local priority
	// 1. Start with user (global) MCP servers
	for k, v := range userConfig.MCPServers {
		merged.MCPServers[k] = v
	}
	if userMCPConfig != nil {
		for k, v := range userMCPConfig.MCPServers {
			merged.MCPServers[k] = v
		}
	}

	// 2. Override with project MCP servers
	for k, v := range projectConfig.MCPServers {
		merged.MCPServers[k] = v
	}
	if projectMCPConfig != nil {
		for k, v := range projectMCPConfig.MCPServers {
			merged.MCPServers[k] = v
		}
	}

	// 3. Override with local MCP servers (highest priority)
	for k, v := range localConfig.MCPServers {
		merged.MCPServers[k] = v
	}
	if localMCPConfig != nil {
		for k, v := range localMCPConfig.MCPServers {
			merged.MCPServers[k] = v
		}
	}

	// Merge context paths (append all, then deduplicate)
	merged.ContextPaths = append([]string{}, userConfig.ContextPaths...)
	merged.ContextPaths = append(merged.ContextPaths, projectConfig.ContextPaths...)
	merged.ContextPaths = append(merged.ContextPaths, localConfig.ContextPaths...)

	// Deduplicate context paths
	seen := make(map[string]bool)
	unique := []string{}
	for _, path := range merged.ContextPaths {
		if !seen[path] {
			seen[path] = true
			unique = append(unique, path)
		}
	}
	merged.ContextPaths = unique

	// Merge Models config (local > project > user)
	// Start with user config, overlay with project, then local
	merged.Models = mergeModelsConfig(userConfig.Models, projectConfig.Models, localConfig.Models)

	return merged
}

// getDefaultWorktreeDir returns the default worktree directory (~/.reliant/worktrees/).
// Environment variable RELIANT_WORKTREE_DIR can override this.
func getDefaultWorktreeDir() string {
	if worktreeDir := os.Getenv("RELIANT_WORKTREE_DIR"); worktreeDir != "" {
		return worktreeDir
	}
	return filepath.Join(config.GetUserConfigDir(), "worktrees")
}

// newDefaultConfig creates a new config with default values
func (l *Loader) newDefaultConfig() *config.Config {
	return &config.Config{
		Data: config.Data{
			Directory: ".reliant",
		},
		WorktreeDir:  getDefaultWorktreeDir(),
		MCPServers:   make(map[string]config.MCPServer),
		Debug:        l.opts.Debug,
		ContextPaths: append([]string{}, config.DefaultContextPaths...),
	}
}

// validateConfig validates a configuration
func (l *Loader) validateConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// Set default MCP type if not specified
	for k, v := range cfg.MCPServers {
		if v.Type == "" {
			v.Type = config.MCPStdio
			cfg.MCPServers[k] = v
		}
	}

	return nil
}

// expandEnvVarsLoader expands environment variables in Claude format (${VAR} or ${VAR:-default})
// using process environment variables.
func expandEnvVarsLoader(s string) string {
	return expandEnvVarsWithLookup(s, func(key string) (string, bool) {
		value := os.Getenv(key)
		if value == "" {
			return "", false
		}
		return value, true
	})
}

// expandEnvVarsWithLookup expands environment variables in Claude format (${VAR} or ${VAR:-default})
// using the provided lookup function.
func expandEnvVarsWithLookup(s string, lookup func(key string) (string, bool)) string {
	result := s

	for {
		start := strings.Index(result, "${")
		if start == -1 {
			break
		}

		end := strings.Index(result[start:], "}")
		if end == -1 {
			break
		}
		end += start

		varExpr := result[start+2 : end]
		if colonIdx := strings.Index(varExpr, ":-"); colonIdx != -1 {
			varName := varExpr[:colonIdx]
			defaultValue := varExpr[colonIdx+2:]

			if value, ok := lookup(varName); ok {
				result = result[:start] + value + result[end+1:]
			} else {
				result = result[:start] + defaultValue + result[end+1:]
			}
		} else {
			if value, ok := lookup(varExpr); ok {
				result = result[:start] + value + result[end+1:]
			} else {
				result = result[:start] + result[end+1:]
			}
		}
	}

	return result
}

// mergeModelsConfig returns the models config from the user (global) scope only.
// Models configuration is intentionally NOT merged from project or local scopes because:
// - Reliant runs as a single server with multiple project windows
// - Local model servers (Ollama, etc.) are machine-specific, not project-specific
// - Custom models and tag preferences should be consistent across all projects
//
// Project and local parameters are kept for API compatibility but ignored.
func mergeModelsConfig(user, project, local *models.UserModelsConfig) *models.UserModelsConfig {
	// Models config only comes from user (global) config
	// Project and local configs are ignored for models
	return user
}
