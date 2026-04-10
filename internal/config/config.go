// Copyright (c) 2025 Reliant Labs
// Package config manages application configuration from various sources.
package config

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// MCPType defines the type of MCP (Model Control Protocol) server.
type MCPType string

// Supported MCP types
const (
	MCPStdio MCPType = "stdio"
	MCPSse   MCPType = "sse"
	MCPHTTP  MCPType = "http"
)

// MCPServer defines the configuration for a Model Control Protocol server.
type MCPServer struct {
	Command string            `yaml:"command" json:"command"`
	Env     []string          `yaml:"env" json:"env"`
	Args    []string          `yaml:"args" json:"args"`
	Type    MCPType           `yaml:"type" json:"type"`
	URL     string            `yaml:"url" json:"url"`
	Headers map[string]string `yaml:"headers" json:"headers"`
	Enabled bool              `yaml:"enabled" json:"enabled"`
}

// Data defines storage configuration.
type Data struct {
	Directory string `yaml:"directory,omitempty" json:"directory,omitempty"`
}

// Config is the main configuration structure for the application.
type Config struct {
	Data            Data                     `yaml:"data" json:"data"`
	WorkingDir      string                   `yaml:"wd,omitempty" json:"wd,omitempty"`
	WorktreeDir     string                   `yaml:"worktreeDir,omitempty" json:"worktreeDir,omitempty"`
	MCPServers      map[string]MCPServer     `yaml:"mcpServers,omitempty" json:"mcpServers,omitempty"`
	Debug           bool                     `yaml:"debug,omitempty" json:"debug,omitempty"`
	ContextPaths    []string                 `yaml:"contextPaths,omitempty" json:"contextPaths,omitempty"`
	Models          *models.UserModelsConfig `yaml:"models,omitempty" json:"models,omitempty"`
	GlobalMemoryMD  string                   `yaml:"-" json:"-"`
	ProjectMemoryMD string                   `yaml:"-" json:"-"`
	// Skills is populated from the daemon's project config sync. It is the
	// authoritative, in-memory snapshot of SKILL.md files available to this
	// project and is what the skill tool / auto-suggestion use at runtime.
	Skills []StoredSkill `yaml:"-" json:"-"`
}

// Application constants
const (
	MaxTokensFallbackDefault = 32000
)

// DefaultContextPaths defines the context/instruction files that are auto-loaded.
// Simplified to only support reliant.md (project instructions) and reliant.local.md (local/gitignored).
var DefaultContextPaths = []string{
	"reliant.md",       // Project instructions (committed to git)
	"reliant.local.md", // Local instructions (gitignored)
}

// Validate checks if the configuration is valid and applies defaults where needed.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	return nil
}
