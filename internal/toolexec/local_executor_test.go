package toolexec

import (
	"context"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

type recordingMCPRuntime struct {
	ensureCalls      []string
	listProjectCalls []string
	callSessions     []string
	toolsByProject   map[string]map[string][]mcp.Tool
}

func (r *recordingMCPRuntime) EnsureProjectServersLoaded(_ context.Context, projectPath string) *mcp.ProjectServerLoadResult {
	r.ensureCalls = append(r.ensureCalls, projectPath)
	return &mcp.ProjectServerLoadResult{}
}

func (r *recordingMCPRuntime) ListProjectTools(projectPath string) (map[string][]mcp.Tool, error) {
	r.listProjectCalls = append(r.listProjectCalls, projectPath)
	if r.toolsByProject == nil {
		return map[string][]mcp.Tool{}, nil
	}
	if toolsForProject, ok := r.toolsByProject[projectPath]; ok {
		return toolsForProject, nil
	}
	return map[string][]mcp.Tool{}, nil
}

func (r *recordingMCPRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return map[string][]mcp.Tool{}, nil
}

func (r *recordingMCPRuntime) ProjectCallTool(session, projectPath, serverName, toolName string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	r.callSessions = append(r.callSessions, session)
	return &mcp.ToolResult{Content: []mcp.ToolContent{{Type: "text", Text: projectPath + ":" + serverName + ":" + toolName}}}, nil
}

func (r *recordingMCPRuntime) CallTool(session, serverName, toolName string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	r.callSessions = append(r.callSessions, session)
	return &mcp.ToolResult{Content: []mcp.ToolContent{{Type: "text", Text: serverName + ":" + toolName}}}, nil
}

type stubMCPBinder struct {
	runtime mcp.Runtime
}

func (b stubMCPBinder) Bind(toolCtx *rctx.ToolContext) *rctx.ToolContext {
	if toolCtx == nil {
		return nil
	}
	return toolCtx.WithMCP(b.runtime)
}

type impossibleTool struct{}

func (impossibleTool) Name() string                    { return "test_impossible" }
func (impossibleTool) Description() string             { return "should not run" }
func (impossibleTool) ParamSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "object"} }
func (impossibleTool) RequiresPermission(*rctx.ToolContext, tools.ToolCall) (bool, error) {
	return false, nil
}
func (impossibleTool) Run(*rctx.ToolContext, tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextErrorResponse("unexpected execution"), nil
}

func TestLocalToolExecutor_EnsuresProjectMCPServersLoadedBeforeLookup(t *testing.T) {
	const worktreePath = "/tmp/reliant-worktree"

	runtime := &recordingMCPRuntime{
		toolsByProject: map[string]map[string][]mcp.Tool{
			worktreePath: {
				"fetch": {
					{Name: "fetch", Description: "Fetch content", InputSchema: map[string]interface{}{"type": "object"}},
				},
			},
		},
	}

	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	executor.SetMCPContextBinder(stubMCPBinder{runtime: runtime})

	result := executor.ExecuteTool(
		context.Background(),
		"mcp__fetch__fetch",
		`{"url":"https://example.com"}`,
		"call-1",
		0,
		map[string]interface{}{
			"chat_id": "chat-1",
			"thread":  "thread-1",
			"project": map[string]interface{}{
				"id":   "project-1",
				"path": "/tmp/project-root",
				"name": "project",
			},
			"worktree": map[string]interface{}{
				"id":   "worktree-1",
				"path": worktreePath,
			},
		},
	)

	require.True(t, result.Success)
	require.False(t, result.IsError)
	require.Equal(t, []string{worktreePath}, runtime.ensureCalls)
	require.Equal(t, []string{worktreePath}, runtime.listProjectCalls)
	require.Equal(t, worktreePath+":fetch:fetch", result.Content)
	// The agent thread must survive the whole executor path to the MCP runtime:
	// it is the key that keeps concurrent fan-out threads off each other's
	// browser page (internal/mcp/session.go).
	require.Equal(t, []string{"thread-1"}, runtime.callSessions,
		"the executing thread must reach the MCP runtime as the session key")
}

func TestLocalToolExecutor_DoesNotAutoloadForNonMCPTools(t *testing.T) {
	runtime := &recordingMCPRuntime{}
	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	executor.SetMCPContextBinder(stubMCPBinder{runtime: runtime})

	result := executor.ExecuteTool(
		context.Background(),
		"test_impossible",
		`{}`,
		"call-2",
		0,
		map[string]interface{}{
			"chat_id": "chat-2",
			"thread":  "thread-2",
			"project": map[string]interface{}{
				"id":   "project-2",
				"path": "/tmp/project-root",
				"name": "project",
			},
		},
	)

	require.False(t, result.Success)
	require.Equal(t, "TOOL_NOT_FOUND", result.ErrorCode)
	require.Empty(t, runtime.ensureCalls)
}
