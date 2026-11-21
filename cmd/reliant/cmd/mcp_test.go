// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// fakeMCP is MCPService as Settings -> MCP servers uses it.
type fakeMCP struct {
	reliantv1connect.UnimplementedMCPServiceHandler
	bearer    string
	projects  []string
	restarted []string
}

func (f *fakeMCP) ListServers(_ context.Context, req *connect.Request[reliantv1.ListServersRequest]) (*connect.Response[reliantv1.ListServersResponse], error) {
	f.bearer = req.Header().Get("Authorization")
	f.projects = append(f.projects, req.Msg.GetProjectId())
	lastErr := `MCP server "lsp" exited before completing the handshake.`
	return connect.NewResponse(&reliantv1.ListServersResponse{Servers: []*reliantv1.MCPServer{
		{Name: "tickets", Enabled: true, Status: reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, ToolCount: 1,
			Scope:  reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
			Config: &reliantv1.MCPServerConfig{Command: "python3", Args: []string{"${projectPath}/tools/tickets_mcp.py"}}},
		{Name: "lsp", Enabled: true, Status: reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNHEALTHY, LastError: &lastErr,
			Scope:  reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL,
			Config: &reliantv1.MCPServerConfig{Command: "gopls", Args: []string{"mcp"}}},
		{Name: "docs", Enabled: false, Scope: reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL,
			Config: &reliantv1.MCPServerConfig{Type: "http", Url: proto3str("https://docs.example.test/mcp")}},
	}}), nil
}

func (f *fakeMCP) GetServerTools(_ context.Context, req *connect.Request[reliantv1.GetServerToolsRequest]) (*connect.Response[reliantv1.GetServerToolsResponse], error) {
	if req.Msg.GetName() != "tickets" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&reliantv1.GetServerToolsResponse{Tools: []*reliantv1.MCPTool{
		{Name: "ticket_status", Description: "Status of one ticket.\nLonger text."},
	}, Total: 1}), nil
}

func (f *fakeMCP) RestartServer(_ context.Context, req *connect.Request[reliantv1.RestartServerRequest]) (*connect.Response[reliantv1.RestartServerResponse], error) {
	f.restarted = append(f.restarted, req.Msg.GetProjectId()+"/"+req.Msg.GetName())
	if req.Msg.GetName() == "lsp" {
		return connect.NewResponse(&reliantv1.RestartServerResponse{Success: false, Message: "Failed to restart server: command not found"}), nil
	}
	return connect.NewResponse(&reliantv1.RestartServerResponse{Success: true, Message: "Server restarted successfully", Name: req.Msg.GetName()}), nil
}

type fakeMCPProjects struct {
	reliantv1connect.UnimplementedProjectServiceHandler
}

func (fakeMCPProjects) ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProjectsResponse{Projects: []*reliantv1.Project{
		{Id: "p-shop", Name: "shop", Path: "/home/me/shop"},
		{Id: "p-api", Name: "api", Path: "/home/me/api"},
	}}), nil
}

func proto3str(s string) *string { return &s }

func newMCPHarness(t *testing.T) *fakeMCP {
	t.Helper()
	isolateCLI(t)
	fake := &fakeMCP{}
	mux := http.NewServeMux()
	p1, h1 := reliantv1connect.NewMCPServiceHandler(fake)
	mux.Handle(p1, h1)
	p2, h2 := reliantv1connect.NewProjectServiceHandler(fakeMCPProjects{})
	mux.Handle(p2, h2)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Setenv(daemonpolicy.AgentShellEnv, "")
	return fake
}

func TestMCPListShowsTheProjectsServers(t *testing.T) {
	fake := newMCPHarness(t)

	out, err := executeRoot(t, "mcp", "list", "--project", "shop")
	require.NoError(t, err)
	assert.Equal(t, "Bearer rlat_login", fake.bearer)
	assert.Equal(t, []string{"p-shop"}, fake.projects, "--project resolves a name to its id")
	assert.Contains(t, out, "SERVER")
	assert.Regexp(t, `docs\s+disabled\s+0\s+local\s+https://docs.example.test/mcp`, out)
	assert.Regexp(t, `lsp\s+unhealthy\s+0\s+user\s+gopls mcp`, out)
	assert.Regexp(t, `tickets\s+healthy\s+1\s+project\s+python3 \$\{projectPath\}/tools/tickets_mcp.py`, out)
	assert.Contains(t, out, `lsp: MCP server "lsp" exited before completing the handshake.`)

	out, err = executeRoot(t, "mcp", "list", "--project", "/home/me/api", "--json")
	require.NoError(t, err)
	var doc struct {
		ProjectID string         `json:"project_id"`
		Servers   []mcpServerRow `json:"servers"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	assert.Equal(t, "p-api", doc.ProjectID)
	require.Len(t, doc.Servers, 3)
	assert.Equal(t, mcpServerRow{Name: "docs", Status: "disabled", Scope: "local", Transport: "http", Target: "https://docs.example.test/mcp"}, doc.Servers[0])

	_, err = executeRoot(t, "mcp", "list", "--project", "nope")
	assert.ErrorContains(t, err, `no project of yours is named "nope"`)
}

func TestMCPToolsNamesWhatAnAgentCalls(t *testing.T) {
	newMCPHarness(t)

	out, err := executeRoot(t, "mcp", "tools", "tickets", "--project", "p-shop")
	require.NoError(t, err)
	assert.Regexp(t, `ticket_status\s+mcp__tickets__ticket_status\s+Status of one ticket\.\n`, out)
	assert.NotContains(t, out, "Longer text")

	out, err = executeRoot(t, "mcp", "tools", "tickets", "--project", "p-shop", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"agent_name": "mcp__tickets__ticket_status"`)

	_, err = executeRoot(t, "mcp", "tools", "missing", "--project", "p-shop")
	assert.ErrorContains(t, err, `no MCP server "missing" is configured for this project`)
}

func TestMCPRestart(t *testing.T) {
	fake := newMCPHarness(t)

	out, err := executeRoot(t, "mcp", "restart", "tickets", "--project", "shop")
	require.NoError(t, err)
	assert.Contains(t, out, "tickets: Server restarted successfully")

	_, err = executeRoot(t, "mcp", "restart", "lsp", "--project", "shop")
	assert.ErrorContains(t, err, "lsp did not restart: Failed to restart server: command not found")
	assert.Equal(t, []string{"p-shop/tickets", "p-shop/lsp"}, fake.restarted)

	// An agent's shell may look, and is refused the restart before any call.
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	_, err = executeRoot(t, "mcp", "restart", "tickets", "--project", "shop")
	assert.ErrorIs(t, err, errAgentCannotRestartMCP)
	assert.Len(t, fake.restarted, 2)
	_, err = executeRoot(t, "mcp", "list", "--project", "shop")
	assert.NoError(t, err)
}
