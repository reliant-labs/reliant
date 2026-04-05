package tools

// MCPToolProvider manages MCP tools for agents (project-scoped).
type MCPToolProvider struct {
	projectPath string
}

// NewMCPToolProvider creates a new unscoped MCP tool provider.
func NewMCPToolProvider() *MCPToolProvider {
	return &MCPToolProvider{}
}

// NewProjectMCPToolProvider creates a project-scoped MCP tool provider.
func NewProjectMCPToolProvider(projectPath string) *MCPToolProvider {
	return &MCPToolProvider{projectPath: projectPath}
}

// GetTools returns all available MCP tools from this provider for the given runtime.
func (p *MCPToolProvider) GetTools(runtime MCPRuntime) []Tool {
	if p == nil || runtime == nil {
		return []Tool{}
	}
	registry := NewMCPToolRegistry(runtime, p.projectPath)
	return registry.GetTools()
}

// RefreshTools exists for API compatibility; registries are built per call.
func (p *MCPToolProvider) RefreshTools(_ MCPRuntime) error {
	return nil
}

// ProjectPath returns the provider project scope (empty for global/unscoped providers).
func (p *MCPToolProvider) ProjectPath() string {
	if p == nil {
		return ""
	}
	return p.projectPath
}
