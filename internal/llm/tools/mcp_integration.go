// Copyright (c) 2025 Reliant Labs
package tools

import (
	"github.com/reliant-labs/reliant/internal/mcp"
)

// MCPToolProvider manages MCP tools for agents (project-scoped)
type MCPToolProvider struct {
	manager     *mcp.Manager
	projectPath string
	registry    *MCPToolRegistry
}

// NewMCPToolProvider creates a new project-scoped MCP tool provider
func NewMCPToolProvider(manager *mcp.Manager) *MCPToolProvider {
	if manager == nil {
		return nil
	}

	provider := &MCPToolProvider{
		manager:  manager,
		registry: NewMCPToolRegistry(manager),
	}

	return provider
}

// NewProjectMCPToolProvider creates a project-scoped MCP tool provider.
func NewProjectMCPToolProvider(manager *mcp.Manager, projectPath string) *MCPToolProvider {
	if manager == nil {
		return nil
	}

	provider := &MCPToolProvider{
		manager:     manager,
		projectPath: projectPath,
		registry:    NewProjectMCPToolRegistry(manager, projectPath),
	}

	return provider
}

// GetTools returns all available MCP tools from this provider
func (p *MCPToolProvider) GetTools() []Tool {
	if p == nil || p.registry == nil {
		return []Tool{}
	}
	return p.registry.GetTools()
}

// RefreshTools refreshes the MCP tool registry
func (p *MCPToolProvider) RefreshTools() error {
	if p == nil || p.registry == nil {
		return nil
	}
	return p.registry.RefreshTools()
}

// GetManager returns the MCP manager instance
func (p *MCPToolProvider) GetManager() *mcp.Manager {
	if p == nil {
		return nil
	}
	return p.manager
}

// ProjectPath returns the provider project scope (empty for global/unscoped providers).
func (p *MCPToolProvider) ProjectPath() string {
	if p == nil {
		return ""
	}
	return p.projectPath
}
