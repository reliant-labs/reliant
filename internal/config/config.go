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

type SkillsSupportingFilesConfig struct {
	MaxFiles int `yaml:"maxFiles,omitempty" json:"maxFiles,omitempty"`
	MaxBytes int `yaml:"maxBytes,omitempty" json:"maxBytes,omitempty"`
}

type SkillsRetrievalConfig struct {
	MaxFiles       int `yaml:"maxFiles,omitempty" json:"maxFiles,omitempty"`
	MaxChunks      int `yaml:"maxChunks,omitempty" json:"maxChunks,omitempty"`
	ChunkBytes     int `yaml:"chunkBytes,omitempty" json:"chunkBytes,omitempty"`
	ChunkOverlap   int `yaml:"chunkOverlap,omitempty" json:"chunkOverlap,omitempty"`
	MaxPromptBytes int `yaml:"maxPromptBytes,omitempty" json:"maxPromptBytes,omitempty"`
}

type SkillsAvailableSkillsConfig struct {
	MaxCount       int `yaml:"maxCount,omitempty" json:"maxCount,omitempty"`
	MaxPromptBytes int `yaml:"maxPromptBytes,omitempty" json:"maxPromptBytes,omitempty"`
}

type SkillsConfig struct {
	ActivationMode  string                      `yaml:"activationMode,omitempty" json:"activationMode,omitempty"`
	IntegrationMode string                      `yaml:"integrationMode,omitempty" json:"integrationMode,omitempty"`
	SupportingFiles SkillsSupportingFilesConfig `yaml:"supportingFiles,omitempty" json:"supportingFiles,omitempty"`
	Retrieval       SkillsRetrievalConfig       `yaml:"retrieval,omitempty" json:"retrieval,omitempty"`
	AvailableSkills SkillsAvailableSkillsConfig `yaml:"availableSkills,omitempty" json:"availableSkills,omitempty"`
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
	Skills          SkillsConfig             `yaml:"skills,omitempty" json:"skills,omitempty"`
	GlobalMemoryMD  string                   `yaml:"-" json:"-"`
	ProjectMemoryMD string                   `yaml:"-" json:"-"`
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
