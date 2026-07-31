package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// MCPContextBinder attaches an execution-time MCP runtime to a tool context.
type MCPContextBinder interface {
	Bind(toolCtx *rctx.ToolContext) *rctx.ToolContext
}

// MCPContextBinderFunc adapts a function to MCPContextBinder.
type MCPContextBinderFunc func(toolCtx *rctx.ToolContext) *rctx.ToolContext

func (f MCPContextBinderFunc) Bind(toolCtx *rctx.ToolContext) *rctx.ToolContext {
	if f == nil {
		return toolCtx
	}
	return f(toolCtx)
}

// NewLocalMCPContextBinder binds a local manager-backed MCP runtime.
func NewLocalMCPContextBinder(runtime mcp.Runtime) MCPContextBinder {
	return MCPContextBinderFunc(func(toolCtx *rctx.ToolContext) *rctx.ToolContext {
		if toolCtx == nil || runtime == nil {
			return toolCtx
		}
		return toolCtx.WithMCP(runtime)
	})
}

// NewDaemonMCPContextBinder binds a daemon-backed MCP runtime resolved from execution context.
func NewDaemonMCPContextBinder(router DaemonRouter) MCPContextBinder {
	return MCPContextBinderFunc(func(toolCtx *rctx.ToolContext) *rctx.ToolContext {
		if toolCtx == nil || router == nil {
			return toolCtx
		}
		userID, ok := auth.GetUserIDFromContext(toolCtx.Context)
		if !ok || userID == "" {
			return toolCtx
		}
		return toolCtx.WithMCP(&daemonMCPRuntime{router: router, userID: userID})
	})
}

type daemonMCPRuntime struct {
	router DaemonRouter
	userID string
}

func (r *daemonMCPRuntime) EnsureProjectServersLoaded(ctx context.Context, projectPath string) *mcp.ProjectServerLoadResult {
	result := &mcp.ProjectServerLoadResult{
		LoadedServers: []string{},
		FailedServers: []string{},
		Errors:        make(map[string]error),
	}
	if projectPath == "" {
		return result
	}

	payload, err := json.Marshal(map[string]string{"project_path": projectPath})
	if err != nil {
		result.FailedServers = append(result.FailedServers, projectPath)
		result.Errors[projectPath] = err
		return result
	}
	respData, err := r.router.SendDaemonCommand(ctx, r.userID, "mcp.ensure_loaded", payload, int32((120 * time.Second).Milliseconds()))
	if err != nil {
		result.FailedServers = append(result.FailedServers, projectPath)
		result.Errors[projectPath] = err
		return result
	}
	var resp struct {
		LoadedServers []string          `json:"loaded_servers"`
		FailedServers []string          `json:"failed_servers"`
		Errors        map[string]string `json:"errors,omitempty"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		result.FailedServers = append(result.FailedServers, projectPath)
		result.Errors[projectPath] = err
		return result
	}
	result.LoadedServers = resp.LoadedServers
	result.FailedServers = resp.FailedServers
	for name, msg := range resp.Errors {
		result.Errors[name] = fmt.Errorf("%s", msg)
	}
	return result
}

func (r *daemonMCPRuntime) ListProjectTools(projectPath string) (map[string][]mcp.Tool, error) {
	return r.listTools(projectPath)
}

func (r *daemonMCPRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return r.listTools("")
}

func (r *daemonMCPRuntime) ProjectCallTool(session, projectPath, serverName, toolName string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	return r.callTool(session, projectPath, serverName, toolName, arguments)
}

func (r *daemonMCPRuntime) CallTool(session, serverName, toolName string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	return r.callTool(session, "", serverName, toolName, arguments)
}

func (r *daemonMCPRuntime) callTool(session, projectPath, serverName, toolName string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"session_key":  session,
		"project_path": projectPath,
		"server_name":  serverName,
		"tool_name":    toolName,
		"arguments":    arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal mcp call payload: %w", err)
	}
	respData, err := r.router.SendDaemonCommand(context.Background(), r.userID, "mcp.call_tool", payload, int32((120 * time.Second).Milliseconds()))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result *mcp.ToolResult `json:"result,omitempty"`
		Error  string          `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal mcp.call_tool response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	if resp.Result == nil {
		return nil, fmt.Errorf("empty tool result from daemon")
	}
	return resp.Result, nil
}

func (r *daemonMCPRuntime) listTools(projectPath string) (map[string][]mcp.Tool, error) {
	payload, err := json.Marshal(map[string]string{"project_path": projectPath})
	if err != nil {
		return nil, fmt.Errorf("marshal mcp server status payload: %w", err)
	}
	respData, err := r.router.SendDaemonCommand(context.Background(), r.userID, "mcp.server_status", payload, int32((10 * time.Second).Milliseconds()))
	if err != nil {
		return nil, err
	}
	var status struct {
		Servers []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(respData, &status); err != nil {
		return nil, fmt.Errorf("unmarshal mcp.server_status response: %w", err)
	}

	result := make(map[string][]mcp.Tool)
	for _, server := range status.Servers {
		listPayload, err := json.Marshal(map[string]string{
			"project_path": projectPath,
			"server_name":  server.Name,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal mcp.list_tools payload: %w", err)
		}
		listRespData, err := r.router.SendDaemonCommand(context.Background(), r.userID, "mcp.list_tools", listPayload, int32((10 * time.Second).Milliseconds()))
		if err != nil {
			return nil, err
		}
		var listResp struct {
			Tools []mcp.Tool `json:"tools"`
			Error string     `json:"error,omitempty"`
		}
		if err := json.Unmarshal(listRespData, &listResp); err != nil {
			return nil, fmt.Errorf("unmarshal mcp.list_tools response: %w", err)
		}
		if listResp.Error != "" {
			return nil, fmt.Errorf("%s", listResp.Error)
		}
		if len(listResp.Tools) > 0 {
			result[server.Name] = listResp.Tools
		}
	}
	return result, nil
}
