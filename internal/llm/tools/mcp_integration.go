package tools

// MCPToolProvider manages MCP tools for agents (project-scoped).
type MCPToolProvider struct {
	projectPath string
	cache       *MCPToolCache
}

// NewMCPToolProvider creates a new unscoped MCP tool provider.
func NewMCPToolProvider() *MCPToolProvider {
	return &MCPToolProvider{}
}

// NewProjectMCPToolProvider creates a project-scoped MCP tool provider.
func NewProjectMCPToolProvider(projectPath string) *MCPToolProvider {
	return &MCPToolProvider{projectPath: projectPath}
}

// WithCache makes the provider serve the cache's last successful listing when
// the runtime cannot list. Registries are built per call, so this is where the
// memory across calls lives.
func (p *MCPToolProvider) WithCache(cache *MCPToolCache) *MCPToolProvider {
	if p != nil {
		p.cache = cache
	}
	return p
}

// GetTools returns all available MCP tools from this provider for the given runtime.
func (p *MCPToolProvider) GetTools(runtime MCPRuntime) []Tool {
	return p.GetToolSet(runtime).Tools
}

// GetToolSet is GetTools with where the tools came from.
func (p *MCPToolProvider) GetToolSet(runtime MCPRuntime) MCPToolSet {
	if p == nil || runtime == nil {
		return MCPToolSet{Tools: []Tool{}, Source: MCPToolsUnrequested}
	}
	return NewMCPToolRegistry(runtime, p.projectPath).WithCache(p.cache).GetToolSet()
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
