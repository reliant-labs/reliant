// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
	mcpclient "github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonMCPProxy implements mcpManagerRuntime by proxying all MCP operations
// to the user's tools daemon via daemon commands. This ensures
// MCP servers run on the user's machine with their PATH, node, npx, etc.
type daemonMCPProxy struct {
	router commandSender
	userID string
	// onChange runs after every start, stop or restart this proxy sends,
	// whatever its outcome: the server's status may have changed.
	onChange func()
}

// commandSender is the one router capability the proxy needs. The handlers
// hand it a projectcheckout.PinnedSender, so every MCP command goes to the
// machine holding the project's checkout.
type commandSender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// newDaemonMCPProxy creates a new daemon-proxying MCP manager.
func newDaemonMCPProxy(router commandSender, userID string) *daemonMCPProxy {
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

// daemonReported is a failure the daemon itself answered a command with: its
// words, from the user's own machine, which the user reads. A failure of the
// round trip is daemonerr's to classify, and its text never reaches the wire.
type daemonReported string

func (e daemonReported) Error() string { return string(e) }

// isDaemonReported reports whether err carries the daemon's own report of a
// failure on the user's machine.
func isDaemonReported(err error) bool {
	var reported daemonReported
	return errors.As(err, &reported)
}

// logServerFailure logs a failure to bring up, or talk to, a user's MCP
// server. The daemon's own report (daemonReported) is that server failing on
// the user's machine — it exited before the handshake, closed the
// connection, its package would not install — which the response already
// tells the user, and only they can fix: a warning. At ERROR each one was an
// issue (prod reliant-api-server be65adaa "MCP server \"tickets\" exited
// before completing the handshake", 2026-11-20). Any other failure is ours
// and stays an ERROR.
func logServerFailure(ctx context.Context, msg string, err error, attrs ...any) {
	attrs = append(attrs, "error", err)
	if isDaemonReported(err) {
		logging.WarnContext(ctx, msg, attrs...)
		return
	}
	logging.ErrorContext(ctx, msg, attrs...)
}

// withDaemonText is msg followed by what the daemon said, when err carries it.
func withDaemonText(msg string, err error) string {
	var reported daemonReported
	if errors.As(err, &reported) {
		return msg + ": " + string(reported)
	}
	if text, ok := toolexec.DaemonReportedText(err); ok {
		return msg + ": " + text
	}
	return msg
}

// --- server status ---

type daemonMCPServerStatus struct {
	Servers []daemonMCPServerStatusEntry `json:"servers"`
}

type daemonMCPServerStatusEntry struct {
	Name             string                `json:"name"`
	Connected        bool                  `json:"connected"`
	Healthy          bool                  `json:"healthy"`
	LastError        string                `json:"last_error,omitempty"`
	ServerInfo       *mcpclient.ServerInfo `json:"server_info,omitempty"`
	ToolCount        int                   `json:"tool_count"`
	ResourcesEnabled bool                  `json:"resources_enabled"`
	PromptsEnabled   bool                  `json:"prompts_enabled"`
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
func (p *daemonMCPProxy) listTools(ctx context.Context, projectPath, serverName string) ([]mcpclient.Tool, error) {
	req := map[string]string{
		"project_path": projectPath,
		"server_name":  serverName,
	}
	respData, err := p.sendCommand(ctx, "mcp.list_tools", req, int32(mcpStatusTimeout.Milliseconds()))
	if err != nil {
		return nil, daemonerr.Error("daemon command mcp.list_tools", err)
	}
	var resp struct {
		Tools []mcpclient.Tool `json:"tools"`
		Error string           `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, daemonReported(resp.Error)
	}
	return resp.Tools, nil
}

// --- mcpManagerRuntime interface ---

func (p *daemonMCPProxy) AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "add", projectPath, serverName, &cfg, false)
}

// StartProjectServerFromProjectConfig starts a server as the machine's own
// copy of the project's MCP config declares it now, with cfg (this process's
// synced copy, which trails an edit) used only by a machine that does not
// know the server, or that predates the request.
func (p *daemonMCPProxy) StartProjectServerFromProjectConfig(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "add", projectPath, serverName, &cfg, true)
}

func (p *daemonMCPProxy) RemoveProjectServer(projectPath, serverName string) error {
	return p.manageServer(context.Background(), "remove", projectPath, serverName, nil, false)
}

func (p *daemonMCPProxy) RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	return p.manageServer(ctx, "restart", projectPath, serverName, &cfg, false)
}

func (p *daemonMCPProxy) manageServer(ctx context.Context, action, projectPath, serverName string, cfg *config.MCPServer, preferProjectConfig bool) error {
	if p.onChange != nil {
		defer p.onChange()
	}
	req := struct {
		Action      string            `json:"action"`
		ProjectPath string            `json:"project_path"`
		ServerName  string            `json:"server_name"`
		Config      *config.MCPServer `json:"config,omitempty"`
		// PreferProjectConfig: see the daemon's mcpManageServerRequest.
		PreferProjectConfig bool `json:"prefer_project_config,omitempty"`
	}{
		Action:              action,
		ProjectPath:         projectPath,
		ServerName:          serverName,
		Config:              cfg,
		PreferProjectConfig: preferProjectConfig,
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
		return daemonReported(resp.Error)
	}
	return nil
}

// EnsureLoaded sends the mcp.ensure_loaded daemon command.
func (p *daemonMCPProxy) EnsureLoaded(ctx context.Context, projectPath string) error {
	req := map[string]string{"project_path": projectPath}
	_, err := p.sendCommand(ctx, "mcp.ensure_loaded", req, int32(mcpCommandTimeout.Milliseconds()))
	return err
}
