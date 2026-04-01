// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonMCPProxy implements mcpManagerRuntime by proxying all MCP operations
// to the user's tools daemon via DaemonRouter daemon commands. This ensures
// MCP servers run on the user's machine with their PATH, node, npx, etc.
type daemonMCPProxy struct {
	router toolexec.DaemonRouter
	userID string
}

// NewDaemonMCPProxy creates a new daemon-proxying MCP manager.
func NewDaemonMCPProxy(router toolexec.DaemonRouter, userID string) *daemonMCPProxy {
	return &daemonMCPProxy{router: router, userID: userID}
}

const (
	mcpCommandTimeout = 120 * time.Second // MCP server startup can be slow (npx downloads)
	mcpStatusTimeout  = 10 * time.Second
)

func (p *daemonMCPProxy) sendCommand(ctx context.Context, commandType string, payload interface{}, timeoutMs int32) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", commandType, err)
	}
	return p.router.SendDaemonCommand(ctx, p.userID, commandType, data, timeoutMs)
}

// --- mcpManagerRuntime: server status queries ---

type daemonMCPServerStatus struct {
	Servers []daemonMCPServerStatusEntry `json:"servers"`
}

type daemonMCPServerStatusEntry struct {
	Name             string          `json:"name"`
	Connected        bool            `json:"connected"`
	Healthy          bool            `json:"healthy"`
	LastError        string          `json:"last_error,omitempty"`
	ServerInfo       *mcp.ServerInfo `json:"server_info,omitempty"`
	ToolCount        int             `json:"tool_count"`
	ResourcesEnabled bool            `json:"resources_enabled"`
	PromptsEnabled   bool            `json:"prompts_enabled"`
}

func (p *daemonMCPProxy) getServerStatus(projectPath string) (*daemonMCPServerStatus, error) {
	req := map[string]string{"project_path": projectPath}
	respData, err := p.sendCommand(context.Background(), "mcp.server_status", req, int32(mcpStatusTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	var status daemonMCPServerStatus
	if err := json.Unmarshal(respData, &status); err != nil {
		return nil, fmt.Errorf("unmarshal mcp.server_status response: %w", err)
	}
	return &status, nil
}

// daemonMCPClient is a lightweight adapter that implements mcp.Client for
// server-side code that needs to inspect MCP server state. It does NOT proxy
// tool calls — those go through the daemon's tool execution path.
type daemonMCPClient struct {
	entry       daemonMCPServerStatusEntry
	tools       []mcp.Tool // lazily populated
	proxy       *daemonMCPProxy
	projectPath string
}

func (c *daemonMCPClient) Initialize(_ context.Context) error { return nil }
func (c *daemonMCPClient) Close() error                       { return nil }
func (c *daemonMCPClient) IsConnected() bool                  { return c.entry.Connected }
func (c *daemonMCPClient) ServerInfo() *mcp.ServerInfo        { return c.entry.ServerInfo }

func (c *daemonMCPClient) ListTools() ([]mcp.Tool, error) {
	if c.tools != nil {
		return c.tools, nil
	}
	req := map[string]string{
		"project_path": c.projectPath,
		"server_name":  c.entry.Name,
	}
	respData, err := c.proxy.sendCommand(context.Background(), "mcp.list_tools", req, int32(mcpStatusTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []mcp.Tool `json:"tools"`
		Error string     `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	c.tools = resp.Tools
	return c.tools, nil
}

func (c *daemonMCPClient) CallTool(name string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	return nil, fmt.Errorf("tool calls should go through daemon tool execution, not the proxy")
}
func (c *daemonMCPClient) ListResources() ([]mcp.Resource, error) { return nil, nil }
func (c *daemonMCPClient) ReadResource(uri string) (*mcp.ResourceContent, error) {
	return nil, nil
}
func (c *daemonMCPClient) ListPrompts() ([]mcp.Prompt, error) { return nil, nil }
func (c *daemonMCPClient) GetPrompt(name string, args map[string]interface{}) (*mcp.PromptResult, error) {
	return nil, nil
}

// --- mcpManagerRuntime interface ---

func (p *daemonMCPProxy) GetProjectClients(projectPath string) map[string]mcp.Client {
	status, err := p.getServerStatus(projectPath)
	if err != nil {
		logging.Warn("Failed to get MCP server status from daemon", "error", err)
		return nil
	}
	clients := make(map[string]mcp.Client, len(status.Servers))
	for _, entry := range status.Servers {
		clients[entry.Name] = &daemonMCPClient{entry: entry, proxy: p, projectPath: projectPath}
	}
	return clients
}

func (p *daemonMCPProxy) GetProjectHealthStatus(projectPath string) map[string]bool {
	status, err := p.getServerStatus(projectPath)
	if err != nil {
		return nil
	}
	result := make(map[string]bool, len(status.Servers))
	for _, entry := range status.Servers {
		result[entry.Name] = entry.Healthy
	}
	return result
}

func (p *daemonMCPProxy) GetProjectLastError(projectPath, serverName string) error {
	status, err := p.getServerStatus(projectPath)
	if err != nil {
		return err
	}
	for _, entry := range status.Servers {
		if entry.Name == serverName && entry.LastError != "" {
			return fmt.Errorf("%s", entry.LastError)
		}
	}
	return nil
}

func (p *daemonMCPProxy) GetProjectClient(projectPath, serverName string) (mcp.Client, bool) {
	status, err := p.getServerStatus(projectPath)
	if err != nil {
		return nil, false
	}
	for _, entry := range status.Servers {
		if entry.Name == serverName {
			return &daemonMCPClient{entry: entry, proxy: p, projectPath: projectPath}, true
		}
	}
	return nil, false
}

func (p *daemonMCPProxy) AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "add", projectPath, serverName, &cfg)
}

func (p *daemonMCPProxy) RemoveProjectServer(projectPath, serverName string) error {
	return p.manageServer(context.Background(), "remove", projectPath, serverName, nil)
}

func (p *daemonMCPProxy) RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "restart", projectPath, serverName, &cfg)
}

func (p *daemonMCPProxy) AddServer(ctx context.Context, name string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "add", "", name, &cfg)
}

func (p *daemonMCPProxy) RemoveServer(name string) error {
	return p.manageServer(context.Background(), "remove", "", name, nil)
}

func (p *daemonMCPProxy) GetAllClients() map[string]mcp.Client {
	return p.GetProjectClients("")
}

func (p *daemonMCPProxy) GetHealthStatus() map[string]bool {
	return p.GetProjectHealthStatus("")
}

func (p *daemonMCPProxy) GetLastError(name string) error {
	return p.GetProjectLastError("", name)
}

func (p *daemonMCPProxy) GetClient(name string) (mcp.Client, bool) {
	return p.GetProjectClient("", name)
}

func (p *daemonMCPProxy) manageServer(ctx context.Context, action, projectPath, serverName string, cfg *config.MCPServer) error {
	req := struct {
		Action      string            `json:"action"`
		ProjectPath string            `json:"project_path"`
		ServerName  string            `json:"server_name"`
		Config      *config.MCPServer `json:"config,omitempty"`
	}{
		Action:      action,
		ProjectPath: projectPath,
		ServerName:  serverName,
		Config:      cfg,
	}

	respData, err := p.sendCommand(ctx, "mcp.manage_server", req, int32(mcpCommandTimeout.Milliseconds()))
	if err != nil {
		return fmt.Errorf("daemon %s server %q: %w", action, serverName, err)
	}

	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return fmt.Errorf("unmarshal manage_server response: %w", err)
	}
	if !resp.Success && resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// EnsureLoaded sends the mcp.ensure_loaded daemon command.
func (p *daemonMCPProxy) EnsureLoaded(ctx context.Context, projectPath string) error {
	req := map[string]string{"project_path": projectPath}
	_, err := p.sendCommand(ctx, "mcp.ensure_loaded", req, int32(mcpCommandTimeout.Milliseconds()))
	return err
}
