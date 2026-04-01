package config

import (
	"embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed recommended-mcps.yaml
var recommendedMCPsFS embed.FS

// RecommendedMCPConfig represents the structure of recommended-mcps.yaml
type RecommendedMCPConfig struct {
	Recommended []RecommendedMCP `yaml:"recommended"`
}

// ConfigField represents a configuration field that needs user input
type ConfigField struct {
	Key               string `yaml:"key"`
	Label             string `yaml:"label"`
	Type              string `yaml:"type"` // text, password, url
	Required          bool   `yaml:"required"`
	Placeholder       string `yaml:"placeholder,omitempty"`
	HelpText          string `yaml:"helpText,omitempty"`
	ValidationRegex   string `yaml:"validationRegex,omitempty"`
	ValidationMessage string `yaml:"validationMessage,omitempty"`
}

// RecommendedMCP represents a single recommended MCP server
type RecommendedMCP struct {
	Name              string             `yaml:"name"`
	DisplayName       string             `yaml:"displayName"`
	Description       string             `yaml:"description"`
	Category          string             `yaml:"category"`
	SetupRequired     bool               `yaml:"setupRequired,omitempty"`
	SetupInstructions string             `yaml:"setupInstructions,omitempty"`
	ConfigTemplate    string             `yaml:"configTemplate,omitempty"`
	ConfigFields      []ConfigField      `yaml:"configFields,omitempty"`
	DocsURL           string             `yaml:"docsUrl,omitempty"`
	Bundles           *RecommendedBundle `yaml:"bundles,omitempty"`
	Config            MCPServerConfig    `yaml:"config"`
}

type RecommendedBundle struct {
	Skills []RecommendedSkillBundle `yaml:"skills,omitempty"`
}

type RecommendedSkillBundle struct {
	Name           string                 `yaml:"name"`
	Description    string                 `yaml:"description,omitempty"`
	TargetScope    string                 `yaml:"targetScope,omitempty"`
	ConflictPolicy string                 `yaml:"conflictPolicy,omitempty"`
	Files          []RecommendedSkillFile `yaml:"files"`
}

type RecommendedSkillFile struct {
	Path    string `yaml:"path"`
	Content string `yaml:"content"`
}

// MCPServerConfig is the YAML representation (uses map for env)
type MCPServerConfig struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Type    string            `yaml:"type"`
	URL     string            `yaml:"url,omitempty"`
}

// ToMCPServer converts MCPServerConfig to the internal MCPServer format
func (c MCPServerConfig) ToMCPServer() MCPServer {
	server := MCPServer{
		Command: c.Command,
		Args:    c.Args,
		Headers: c.Headers,
		Type:    MCPType(c.Type),
		URL:     c.URL,
	}

	// Convert env from map to []string
	if len(c.Env) > 0 {
		server.Env = make([]string, 0, len(c.Env))
		for k, v := range c.Env {
			server.Env = append(server.Env, k+"="+v)
		}
	}

	return server
}

// LoadRecommendedMCPs loads the recommended MCPs from the embedded YAML file
func LoadRecommendedMCPs() (*RecommendedMCPConfig, error) {
	data, err := recommendedMCPsFS.ReadFile("recommended-mcps.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read recommended-mcps.yaml: %w", err)
	}

	var config RecommendedMCPConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse recommended-mcps.yaml: %w", err)
	}

	return &config, nil
}
