// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonMCPProxy implements mcpManagerRuntime by proxying all MCP operations
// to the user's tools daemon via DaemonRouter daemon commands. This ensures
// MCP servers run on the user's machine with their PATH, node, npx, etc.
type daemonMCPProxy struct {
	router toolexec.DaemonRouter
	userID string
	// onChange runs after every start, stop or restart this proxy sends,
	// whatever its outcome: the server's status may have changed.
	onChange func()
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

// --- server status ---

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

// entry returns the status of the named server, if the daemon reported one.
func (s *daemonMCPServerStatus) entry(name string) (daemonMCPServerStatusEntry, bool) {
	if s == nil {
		return daemonMCPServerStatusEntry{}, false
	}
	for _, e := range s.Servers {
		if e.Name == name {
			return e, true
		}
	}
	return daemonMCPServerStatusEntry{}, false
}

// serverStatus is one mcp.server_status round trip: every server the daemon
// has for the project — running or failed to start — with its health, last
// error, server info and tool count. It is all a listing needs; nothing
// about a server's status takes a second call.
func (p *daemonMCPProxy) serverStatus(ctx context.Context, projectPath string) (*daemonMCPServerStatus, error) {
	req := map[string]string{"project_path": projectPath}
	respData, err := p.sendCommand(ctx, "mcp.server_status", req, int32(mcpStatusTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	var status daemonMCPServerStatus
	if err := json.Unmarshal(respData, &status); err != nil {
		return nil, fmt.Errorf("unmarshal mcp.server_status response: %w", err)
	}
	return &status, nil
}

// listTools asks the daemon for the tools one server exposes.
func (p *daemonMCPProxy) listTools(ctx context.Context, projectPath, serverName string) ([]mcp.Tool, error) {
	req := map[string]string{
		"project_path": projectPath,
		"server_name":  serverName,
	}
	respData, err := p.sendCommand(ctx, "mcp.list_tools", req, int32(mcpStatusTimeout.Milliseconds()))
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
	return resp.Tools, nil
}

// --- mcpManagerRuntime interface ---

func (p *daemonMCPProxy) AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "add", projectPath, serverName, &cfg)
}

func (p *daemonMCPProxy) RemoveProjectServer(projectPath, serverName string) error {
	return p.manageServer(context.Background(), "remove", projectPath, serverName, nil)
}

func (p *daemonMCPProxy) RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "restart", projectPath, serverName, &cfg)
}

func (p *daemonMCPProxy) manageServer(ctx context.Context, action, projectPath, serverName string, cfg *config.MCPServer) error {
	if p.onChange != nil {
		defer p.onChange()
	}
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
