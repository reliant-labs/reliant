package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
	"gopkg.in/yaml.v3"
)

// ProjectRef identifies a project for configuration lookups.
type ProjectRef struct {
	ProjectID string
}

// ConfigProvider abstracts project configuration loading.
type ConfigProvider interface {
	GetProjectConfig(ctx context.Context, ref ProjectRef) (*Config, error)
}

// StoredProjectConfigRecord is a repository-backed config payload.
type StoredProjectConfigRecord struct {
	ProjectID            string
	UserConfigYAML       *string
	ProjectConfigYAML    *string
	LocalConfigYAML      *string
	GlobalMemoryMD       *string
	ProjectMemoryMD      *string
	MCPConfigs           *string // JSON object with scope->mcp.json content
	ProjectWorkflowsJSON *string // JSON array of stored workflows
	ProjectPresetsJSON   *string // JSON array of stored presets
	ProjectScenariosJSON *string // JSON array of stored scenarios
	ProjectSkillsJSON    *string // JSON array of stored skills (SKILL.md)
	RepoMemoriesJSON     *string // JSON object: repo relative path -> memory content
	RuntimeType          *string // Serving daemon's runtime/sandbox type ("kata", "gvisor"); nil for local/unknown
}

// StoredConfigStore reads stored project config records.
type StoredConfigStore interface {
	GetProjectConfigRecord(ctx context.Context, projectID string) (*StoredProjectConfigRecord, error)
}

// StoredConfigProvider loads project config from persisted records.
type StoredConfigProvider struct {
	store StoredConfigStore
}

func NewStoredConfigProvider(store StoredConfigStore) *StoredConfigProvider {
	return &StoredConfigProvider{store: store}
}

func (p *StoredConfigProvider) GetProjectConfig(ctx context.Context, ref ProjectRef) (*Config, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("stored config provider is not initialized")
	}
	if ref.ProjectID == "" {
		return nil, fmt.Errorf("project ID is required for StoredConfigProvider")
	}

	record, err := p.store.GetProjectConfigRecord(ctx, ref.ProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Return a default empty config when the daemon hasn't synced yet.
			// This handles the race where a workflow starts before the daemon
			// sends the initial config snapshot (e.g. new project first chat).
			// The daemon will sync the real config shortly; subsequent LLM calls
			// in the same conversation will pick it up.
			logging.Warn("Config snapshot not yet synced for project, using default empty config",
				"projectID", ref.ProjectID,
			)
			return &Config{}, nil
		}
		return nil, fmt.Errorf("failed to load stored config for project %s: %w", ref.ProjectID, err)
	}

	cfg, err := mergeStoredConfigRecord(record)
	if err != nil {
		return nil, err
	}

	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("stored config validation failed: %w", err)
	}

	return cfg, nil
}

func mergeStoredConfigRecord(record *StoredProjectConfigRecord) (*Config, error) {
	if record == nil {
		return nil, fmt.Errorf("stored project config record is nil")
	}

	cfg := &Config{}

	// Merge in precedence order: user < project < local.
	for _, blob := range []*string{record.UserConfigYAML, record.ProjectConfigYAML, record.LocalConfigYAML} {
		if blob == nil || *blob == "" {
			continue
		}
		partial := &Config{}
		if err := yaml.Unmarshal([]byte(*blob), partial); err != nil {
			return nil, fmt.Errorf("failed to parse stored YAML config: %w", err)
		}

		mergeConfigInto(cfg, partial)
	}

	if record.GlobalMemoryMD != nil {
		cfg.GlobalMemoryMD = strings.TrimSpace(*record.GlobalMemoryMD)
	}
	if record.ProjectMemoryMD != nil {
		cfg.ProjectMemoryMD = strings.TrimSpace(*record.ProjectMemoryMD)
	}

	if skills, err := ParseStoredSkills(record.ProjectSkillsJSON); err != nil {
		return nil, err
	} else {
		cfg.Skills = skills
	}

	if repoMems, err := ParseRepoMemories(record.RepoMemoriesJSON); err != nil {
		return nil, err
	} else if len(repoMems) > 0 {
		cfg.RepoMemories = repoMems
	}

	if record.RuntimeType != nil {
		cfg.DaemonRuntimeType = strings.TrimSpace(*record.RuntimeType)
	}

	if record.MCPConfigs != nil && *record.MCPConfigs != "" {
		var scopeConfigs map[string]string
		if err := json.Unmarshal([]byte(*record.MCPConfigs), &scopeConfigs); err != nil {
			return nil, fmt.Errorf("failed to parse stored MCP configs JSON: %w", err)
		}

		// Merge in scope precedence: user < project < local.
		for _, scope := range []string{"user", "project", "local"} {
			raw, ok := scopeConfigs[scope]
			if !ok || raw == "" {
				continue
			}
			servers := parseMCPServersFromJSON(raw)
			if len(servers) == 0 {
				continue
			}
			if cfg.MCPServers == nil {
				cfg.MCPServers = map[string]MCPServer{}
			}
			for name, server := range servers {
				cfg.MCPServers[name] = server
			}
		}
	}

	return cfg, nil
}

func mergeConfigInto(dst *Config, src *Config) {
	if dst == nil || src == nil {
		return
	}

	if src.Data.Directory != "" {
		dst.Data = src.Data
	}
	if src.WorkingDir != "" {
		dst.WorkingDir = src.WorkingDir
	}
	if src.WorktreeDir != "" {
		dst.WorktreeDir = src.WorktreeDir
	}
	if len(src.ContextPaths) > 0 {
		dst.ContextPaths = append([]string(nil), src.ContextPaths...)
	}
	if src.Models != nil {
		dst.Models = src.Models
	}

	if len(src.MCPServers) > 0 {
		if dst.MCPServers == nil {
			dst.MCPServers = map[string]MCPServer{}
		}
		for name, server := range src.MCPServers {
			dst.MCPServers[name] = server
		}
	}
}

func parseMCPServersFromJSON(raw string) map[string]MCPServer {
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil
	}
	serversRaw, ok := doc["mcpServers"].(map[string]interface{})
	if !ok {
		return nil
	}
	servers := make(map[string]MCPServer)
	for name, cfgRaw := range serversRaw {
		cfgMap, ok := cfgRaw.(map[string]interface{})
		if !ok {
			continue
		}
		server := MCPServer{Type: MCPStdio, Enabled: true}
		if command, ok := cfgMap["command"].(string); ok {
			server.Command = command
		}
		if args, ok := cfgMap["args"].([]interface{}); ok {
			server.Args = make([]string, 0, len(args))
			for _, arg := range args {
				if s, ok := arg.(string); ok {
					server.Args = append(server.Args, s)
				}
			}
		}
		if env, ok := cfgMap["env"].(map[string]interface{}); ok {
			server.Env = make([]string, 0, len(env))
			for k, v := range env {
				server.Env = append(server.Env, fmt.Sprintf("%s=%v", k, v))
			}
		}
		if url, ok := cfgMap["url"].(string); ok {
			server.URL = url
			if server.Command == "" {
				server.Type = MCPSse
			}
		}
		if headers, ok := cfgMap["headers"].(map[string]interface{}); ok {
			server.Headers = make(map[string]string, len(headers))
			for k, v := range headers {
				server.Headers[k] = fmt.Sprintf("%v", v)
			}
		}
		if enabled, ok := cfgMap["enabled"].(bool); ok {
			server.Enabled = enabled
		}
		servers[name] = server
	}
	if len(servers) == 0 {
		return nil
	}
	return servers
}
