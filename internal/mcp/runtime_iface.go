package mcp

import "context"

// Compile-time interface assertion: *Manager must implement Runtime.
var _ Runtime = (*Manager)(nil)

// Runtime is the execution-time MCP interface consumed by tool/runtime code.
// Implementations may be backed by a local Manager or a daemon/NATS proxy.
type Runtime interface {
	EnsureProjectServersLoaded(ctx context.Context, projectPath string) *ProjectServerLoadResult
	ListProjectTools(projectPath string) (map[string][]Tool, error)
	ListAllTools() (map[string][]Tool, error)
	ProjectCallTool(projectPath, serverName, toolName string, arguments map[string]interface{}) (*ToolResult, error)
	CallTool(serverName, toolName string, arguments map[string]interface{}) (*ToolResult, error)
}
