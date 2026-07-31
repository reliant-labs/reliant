package mcp

import "context"

// Compile-time interface assertion: *Manager must implement Runtime.
var _ Runtime = (*Manager)(nil)

// Runtime is the execution-time MCP interface consumed by tool/runtime code.
// Implementations may be backed by a local Manager or a daemon/NATS proxy.
//
// The leading `session` on the call methods identifies the caller whose view of
// a stateful server must stay private — in practice the agent thread. It is
// honoured only by session-scoped servers (see session.go); for every other
// server the callers share one client and the key is ignored. Empty means "no
// session", which is what a CLI or one-off daemon command passes.
type Runtime interface {
	EnsureProjectServersLoaded(ctx context.Context, projectPath string) *ProjectServerLoadResult
	ListProjectTools(projectPath string) (map[string][]Tool, error)
	ListAllTools() (map[string][]Tool, error)
	ProjectCallTool(session, projectPath, serverName, toolName string, arguments map[string]interface{}) (*ToolResult, error)
	CallTool(session, serverName, toolName string, arguments map[string]interface{}) (*ToolResult, error)
}
