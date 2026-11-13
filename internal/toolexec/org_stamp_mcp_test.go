// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
)

// stampSeeingRuntime is an MCP runtime that records the org stamp each load
// was asked under.
type stampSeeingRuntime struct {
	recordingMCPRuntime
	loadStamps []*daemonpolicy.OrgStamp
}

func (r *stampSeeingRuntime) EnsureProjectServersLoaded(ctx context.Context, projectPath string) *mcp.ProjectServerLoadResult {
	r.loadStamps = append(r.loadStamps, daemonpolicy.OrgStampFromContext(ctx))
	return r.recordingMCPRuntime.EnsureProjectServersLoaded(ctx, projectPath)
}

func mcpContextMap(stamp *daemonpolicy.OrgStamp) map[string]interface{} {
	m := map[string]interface{}{
		"chat_id": "chat-1", "thread": "thread-1",
		"project": map[string]interface{}{"id": "p", "path": "/tmp/stamp-project", "name": "p"},
	}
	if v := stamp.ContextValue(); v != nil {
		raw, _ := json.Marshal(v)
		var decoded map[string]interface{}
		_ = json.Unmarshal(raw, &decoded)
		m[daemonpolicy.OrgStampContextKey] = decoded
	}
	return m
}

// A machine that reads the stamp refuses an MCP tool of a server the org does
// not allow before anything starts its server, and runs an allowed one with
// the stamp on the context its server load sees.
func TestLocalExecutor_MCPStampRefusesBeforeSpawn(t *testing.T) {
	runtime := &stampSeeingRuntime{recordingMCPRuntime: recordingMCPRuntime{toolsByProject: map[string]map[string][]mcp.Tool{
		"/tmp/stamp-project": {"fetch": {{Name: "fetch", InputSchema: map[string]interface{}{"type": "object"}}}},
	}}}
	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	executor.SetMCPContextBinder(stubMCPBinder{runtime: runtime})
	stamp := &daemonpolicy.OrgStamp{OrgID: "org-a", MCP: &daemonpolicy.OrgStampMCP{AllowedServers: []string{"fetch"}}}

	refused := executor.ExecuteTool(context.Background(), "mcp__slack__post", `{}`, "c1", 0, mcpContextMap(stamp))
	assert.True(t, refused.IsError)
	assert.Equal(t, ErrorCodeOrgPolicy, refused.ErrorCode)
	assert.Empty(t, runtime.ensureCalls, "a refused call starts no server")
	assert.Empty(t, runtime.callSessions)

	allowed := executor.ExecuteTool(context.Background(), "mcp__fetch__fetch", `{}`, "c2", 0, mcpContextMap(stamp))
	require.True(t, allowed.Success, allowed.Content)
	require.Len(t, runtime.loadStamps, 1)
	require.NotNil(t, runtime.loadStamps[0], "the load runs under the run's stamp")
	assert.False(t, runtime.loadStamps[0].MCPServerAllowed("slack"))
}

// A tool name does not identify its server: with "github" allowed, the
// already-running forbidden server "github__evil" offers mcp__github__evil__post,
// whose name passes the prefix check. The server it resolves to is checked
// before it runs, so it is refused; github's own tool still runs.
func TestLocalExecutor_MCPStampChecksTheResolvedServer(t *testing.T) {
	runtime := &stampSeeingRuntime{recordingMCPRuntime: recordingMCPRuntime{toolsByProject: map[string]map[string][]mcp.Tool{
		"/tmp/stamp-project": {
			"github":       {{Name: "create", InputSchema: map[string]interface{}{"type": "object"}}},
			"github__evil": {{Name: "post", InputSchema: map[string]interface{}{"type": "object"}}},
		},
	}}}
	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	executor.SetMCPContextBinder(stubMCPBinder{runtime: runtime})
	stamp := &daemonpolicy.OrgStamp{OrgID: "org-a", MCP: &daemonpolicy.OrgStampMCP{AllowedServers: []string{"github"}}}

	refused := executor.ExecuteTool(context.Background(), "mcp__github__evil__post", `{}`, "c1", 0, mcpContextMap(stamp))
	assert.True(t, refused.IsError, refused.Content)
	assert.Equal(t, ErrorCodeOrgPolicy, refused.ErrorCode)
	assert.Empty(t, runtime.callSessions, "the forbidden server's tool never ran")

	allowed := executor.ExecuteTool(context.Background(), "mcp__github__create", `{}`, "c2", 0, mcpContextMap(stamp))
	require.True(t, allowed.Success, allowed.Content)
	assert.Len(t, runtime.callSessions, 1)
}

// No stamp: every MCP tool runs as today.
func TestLocalExecutor_NoStampRunsMCPAsToday(t *testing.T) {
	runtime := &stampSeeingRuntime{recordingMCPRuntime: recordingMCPRuntime{toolsByProject: map[string]map[string][]mcp.Tool{
		"/tmp/stamp-project": {"slack": {{Name: "post", InputSchema: map[string]interface{}{"type": "object"}}}},
	}}}
	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	executor.SetMCPContextBinder(stubMCPBinder{runtime: runtime})
	res := executor.ExecuteTool(context.Background(), "mcp__slack__post", `{}`, "c1", 0, mcpContextMap(nil))
	require.True(t, res.Success, res.Content)
	require.Equal(t, []*daemonpolicy.OrgStamp{nil}, runtime.loadStamps)
}

// payloadRouter records the payload of each daemon command.
type payloadRouter struct {
	routerStub
	payloads map[string][]byte
}

func (r *payloadRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	if r.payloads == nil {
		r.payloads = map[string][]byte{}
	}
	r.payloads[commandType] = payload
	return []byte(`{"loaded_servers":[],"failed_servers":[]}`), nil
}

// The worker forwards the run's stamp with mcp.ensure_loaded, so the machine
// starts no server the org does not allow; with no stamp the payload is as
// before.
func TestDaemonMCPRuntime_ForwardsTheStampOnLoad(t *testing.T) {
	stamp := &daemonpolicy.OrgStamp{OrgID: "org-a", MCP: &daemonpolicy.OrgStampMCP{AllowedServers: []string{"fetch"}}}
	router := &payloadRouter{}
	rt := &daemonMCPRuntime{router: router, userID: "u"}
	rt.EnsureProjectServersLoaded(daemonpolicy.WithOrgStamp(context.Background(), stamp), "/p")
	var sent struct {
		ProjectPath string                 `json:"project_path"`
		OrgPolicy   *daemonpolicy.OrgStamp `json:"org_policy"`
	}
	require.NoError(t, json.Unmarshal(router.payloads["mcp.ensure_loaded"], &sent))
	assert.Equal(t, "/p", sent.ProjectPath)
	require.NotNil(t, sent.OrgPolicy)
	assert.Equal(t, []string{"fetch"}, sent.OrgPolicy.MCP.AllowedServers)

	rt.EnsureProjectServersLoaded(context.Background(), "/p")
	assert.JSONEq(t, `{"project_path":"/p"}`, string(router.payloads["mcp.ensure_loaded"]))
}
