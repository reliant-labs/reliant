package tools

import (
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// MCPRuntime is the execution-time MCP capability consumed by tool discovery and adapters.
type MCPRuntime = mcp.Runtime

func runtimeFromToolContext(toolCtx *rctx.ToolContext) MCPRuntime {
	if toolCtx == nil {
		return nil
	}
	return toolCtx.MCP
}
