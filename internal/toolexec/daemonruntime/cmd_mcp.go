// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/gitutil"
	"github.com/reliant-labs/reliant/internal/mcp"
)

// mcpMgr is the package-level MCP manager instance used by MCP runtime commands.
var mcpMgr *mcp.Manager

// SetMCPManager sets the MCP manager used by MCP runtime command handlers.
func SetMCPManager(m *mcp.Manager) {
	mcpMgr = m
}

func init() {
	RegisterCommand("mcp.write_config", handleWriteConfig)
	RegisterCommand("mcp.ensure_gitignore", handleEnsureGitignore)
	RegisterCommand("mcp.install_bundle", handleInstallBundle)
	RegisterCommand("mcp.check_resource", handleCheckResource)
	RegisterCommand("mcp.server_status", handleMCPServerStatus)
	RegisterCommand("mcp.manage_server", handleMCPManageServer)
	RegisterCommand("mcp.list_tools", handleMCPListTools)
	RegisterCommand("mcp.call_tool", handleMCPCallTool)
	RegisterCommand("mcp.ensure_loaded", handleMCPEnsureLoaded)
}

// --- mcp.write_config ---

type writeConfigRequest struct {
	Scope       string `json:"scope"`
	ProjectPath string `json:"project_path"`
	Content     string `json:"content"`
}

type writeConfigResponse struct {
	Success bool   `json:"success"`
	Path    string `json:"path"`
	Error   string `json:"error,omitempty"`
}

func mcpConfigPathForScope(scope, projectPath string) (string, error) {
	switch scope {
	case "user", "global":
		return filepath.Join(config.GetUserConfigDir(), config.MCPConfigFileName), nil
	case "local", "project_local":
		if projectPath == "" {
			return "", fmt.Errorf("project_path is required for local scope")
		}
		return filepath.Join(projectPath, config.ReliantLocalDir, config.MCPConfigFileName), nil
	case "", "project":
		if projectPath == "" {
			return "", fmt.Errorf("project_path is required for project scope")
		}
		return filepath.Join(projectPath, config.ReliantDir, config.MCPConfigFileName), nil
	default:
		return "", fmt.Errorf("unsupported scope: %s", scope)
	}
}

func handleWriteConfig(_ context.Context, payload []byte) ([]byte, error) {
	var req writeConfigRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	mcpPath, err := mcpConfigPathForScope(req.Scope, req.ProjectPath)
	if err != nil {
		return json.Marshal(writeConfigResponse{Error: err.Error()})
	}

	dir := filepath.Dir(mcpPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return json.Marshal(writeConfigResponse{Error: fmt.Sprintf("failed to create config directory: %v", err)})
	}

	if err := os.WriteFile(mcpPath, []byte(req.Content), 0o644); err != nil {
		return json.Marshal(writeConfigResponse{Error: fmt.Sprintf("failed to write mcp.json: %v", err)})
	}

	return json.Marshal(writeConfigResponse{Success: true, Path: mcpPath})
}

// --- mcp.ensure_gitignore ---

type ensureGitignoreRequest struct {
	ProjectPath string `json:"project_path"`
	Pattern     string `json:"pattern"`
}

type ensureGitignoreResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func handleEnsureGitignore(_ context.Context, payload []byte) ([]byte, error) {
	var req ensureGitignoreRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if err := gitutil.EnsureGitignoreContains(req.ProjectPath, req.Pattern); err != nil {
		return json.Marshal(ensureGitignoreResponse{Error: err.Error()})
	}

	return json.Marshal(ensureGitignoreResponse{Success: true})
}

// --- mcp.install_bundle ---

type installBundleFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type installBundleRequest struct {
	TargetDir      string              `json:"target_dir"`
	Files          []installBundleFile `json:"files"`
	ConflictPolicy string              `json:"conflict_policy"`
}

type installBundleResponse struct {
	Installed int    `json:"installed"`
	Skipped   int    `json:"skipped"`
	Error     string `json:"error,omitempty"`
}

func handleInstallBundle(_ context.Context, payload []byte) ([]byte, error) {
	var req installBundleRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	installed := 0
	skipped := 0

	for _, f := range req.Files {
		safePath, err := safeInstallBundlePath(req.TargetDir, f.Path)
		if err != nil {
			return json.Marshal(installBundleResponse{Error: err.Error()})
		}

		if err := os.MkdirAll(filepath.Dir(safePath), 0o755); err != nil {
			return json.Marshal(installBundleResponse{Error: fmt.Sprintf("failed to create directory: %v", err)})
		}

		_, statErr := os.Stat(safePath)
		exists := statErr == nil
		shouldWrite, _ := applyBundleConflictPolicy(exists, req.ConflictPolicy)
		if !shouldWrite {
			skipped++
			continue
		}

		if err := os.WriteFile(safePath, []byte(f.Content), 0o644); err != nil {
			return json.Marshal(installBundleResponse{Error: fmt.Sprintf("failed to write file %s: %v", f.Path, err)})
		}
		installed++
	}

	return json.Marshal(installBundleResponse{Installed: installed, Skipped: skipped})
}

func safeInstallBundlePath(root, rel string) (string, error) {
	cleanRel := filepath.Clean(rel)
	if cleanRel == "." || cleanRel == "" {
		return "", fmt.Errorf("empty bundle file path")
	}
	if filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("absolute bundle file path is not allowed: %s", rel)
	}
	if strings.HasPrefix(cleanRel, "..") || strings.Contains(cleanRel, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("path traversal is not allowed: %s", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(filepath.Join(root, cleanRel))
	if err != nil {
		return "", err
	}
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle path escapes target root: %s", rel)
	}
	return absPath, nil
}

func applyBundleConflictPolicy(existing bool, policy string) (shouldWrite bool, requiresPrompt bool) {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", "skip":
		if existing {
			return false, false
		}
		return true, false
	case "overwrite":
		return true, false
	case "prompt":
		if existing {
			return false, true
		}
		return true, false
	default:
		if existing {
			return false, false
		}
		return true, false
	}
}

// --- mcp.check_resource ---

type checkResourceRequest struct {
	Path string `json:"path"`
}

type checkResourceResponse struct {
	Exists bool   `json:"exists"`
	Error  string `json:"error,omitempty"`
}

func handleCheckResource(_ context.Context, payload []byte) ([]byte, error) {
	var req checkResourceRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	expandedPath := os.ExpandEnv(req.Path)
	_, err := os.Stat(expandedPath)
	exists := err == nil

	return json.Marshal(checkResourceResponse{Exists: exists})
}

// --- mcp.server_status ---

type mcpServerStatusRequest struct {
	ProjectPath string `json:"project_path"`
}

type mcpServerStatusEntry struct {
	Name             string          `json:"name"`
	Connected        bool            `json:"connected"`
	Healthy          bool            `json:"healthy"`
	LastError        string          `json:"last_error,omitempty"`
	ServerInfo       *mcp.ServerInfo `json:"server_info,omitempty"`
	ToolCount        int             `json:"tool_count"`
	ResourcesEnabled bool            `json:"resources_enabled"`
	PromptsEnabled   bool            `json:"prompts_enabled"`
}

type mcpServerStatusResponse struct {
	Servers []mcpServerStatusEntry `json:"servers"`
}

func handleMCPServerStatus(_ context.Context, payload []byte) ([]byte, error) {
	if mcpMgr == nil {
		return json.Marshal(mcpServerStatusResponse{Servers: []mcpServerStatusEntry{}})
	}

	var req mcpServerStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	var clients map[string]mcp.Client
	var healthStatus map[string]bool
	if req.ProjectPath != "" {
		clients = mcpMgr.GetProjectClients(req.ProjectPath)
		healthStatus = mcpMgr.GetProjectHealthStatus(req.ProjectPath)
	} else {
		clients = mcpMgr.GetAllClients()
		healthStatus = mcpMgr.GetHealthStatus()
	}

	entries := make([]mcpServerStatusEntry, 0, len(clients))
	for name, client := range clients {
		entry := mcpServerStatusEntry{
			Name:      name,
			Connected: client.IsConnected(),
			Healthy:   healthStatus[name],
		}
		if info := client.ServerInfo(); info != nil {
			entry.ServerInfo = info
			entry.ResourcesEnabled = info.Capabilities.Resources != nil
			entry.PromptsEnabled = info.Capabilities.Prompts != nil
		}
		if tools, err := client.ListTools(); err == nil {
			entry.ToolCount = len(tools)
		}

		var lastErr error
		if req.ProjectPath != "" {
			lastErr = mcpMgr.GetProjectLastError(req.ProjectPath, name)
		} else {
			lastErr = mcpMgr.GetLastError(name)
		}
		if lastErr != nil {
			entry.LastError = lastErr.Error()
		}

		entries = append(entries, entry)
	}

	// Also include servers that have errors but no running client
	// (they failed to start and are in healthChecks but not in clients)
	healthAll := mcpMgr.GetHealthStatus()
	for name := range healthAll {
		if _, exists := clients[name]; exists {
			continue // already included
		}
		var lastErr error
		if req.ProjectPath != "" {
			lastErr = mcpMgr.GetProjectLastError(req.ProjectPath, name)
		} else {
			lastErr = mcpMgr.GetLastError(name)
		}
		if lastErr != nil {
			entries = append(entries, mcpServerStatusEntry{
				Name:      name,
				Connected: false,
				Healthy:   false,
				LastError: lastErr.Error(),
			})
		}
	}

	return json.Marshal(mcpServerStatusResponse{Servers: entries})
}

// --- mcp.manage_server ---

type mcpManageServerRequest struct {
	Action      string            `json:"action"` // "add", "remove", "restart"
	ProjectPath string            `json:"project_path"`
	ServerName  string            `json:"server_name"`
	Config      *config.MCPServer `json:"config,omitempty"` // required for add/restart
}

type mcpManageServerResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func handleMCPManageServer(ctx context.Context, payload []byte) ([]byte, error) {
	if mcpMgr == nil {
		return json.Marshal(mcpManageServerResponse{Error: "MCP manager not initialized"})
	}

	var req mcpManageServerRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	var err error
	switch req.Action {
	case "add":
		if req.Config == nil {
			return json.Marshal(mcpManageServerResponse{Error: "config is required for add action"})
		}
		if req.ProjectPath != "" {
			err = mcpMgr.AddProjectServer(ctx, req.ProjectPath, req.ServerName, *req.Config)
		} else {
			err = mcpMgr.AddServer(ctx, req.ServerName, *req.Config)
		}
	case "remove":
		if req.ProjectPath != "" {
			err = mcpMgr.RemoveProjectServer(req.ProjectPath, req.ServerName)
		} else {
			err = mcpMgr.RemoveServer(req.ServerName)
		}
	case "restart":
		if req.Config == nil {
			return json.Marshal(mcpManageServerResponse{Error: "config is required for restart action"})
		}
		if req.ProjectPath != "" {
			err = mcpMgr.RestartProjectServer(ctx, req.ProjectPath, req.ServerName, *req.Config)
		} else {
			// restart = remove + add
			_ = mcpMgr.RemoveServer(req.ServerName)
			err = mcpMgr.AddServer(ctx, req.ServerName, *req.Config)
		}
	default:
		return json.Marshal(mcpManageServerResponse{Error: fmt.Sprintf("unknown action: %s", req.Action)})
	}

	if err != nil {
		return json.Marshal(mcpManageServerResponse{Error: err.Error()})
	}
	return json.Marshal(mcpManageServerResponse{Success: true})
}

// --- mcp.list_tools ---

type mcpListToolsRequest struct {
	ProjectPath string `json:"project_path"`
	ServerName  string `json:"server_name"`
}

type mcpListToolsResponse struct {
	Tools []mcp.Tool `json:"tools"`
	Error string     `json:"error,omitempty"`
}

func handleMCPListTools(_ context.Context, payload []byte) ([]byte, error) {
	if mcpMgr == nil {
		return json.Marshal(mcpListToolsResponse{Error: "MCP manager not initialized"})
	}

	var req mcpListToolsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	var client mcp.Client
	var exists bool
	if req.ProjectPath != "" {
		client, exists = mcpMgr.GetProjectClient(req.ProjectPath, req.ServerName)
	} else {
		client, exists = mcpMgr.GetClient(req.ServerName)
	}
	if !exists {
		return json.Marshal(mcpListToolsResponse{Error: fmt.Sprintf("server %q not found", req.ServerName)})
	}

	tools, err := client.ListTools()
	if err != nil {
		return json.Marshal(mcpListToolsResponse{Error: err.Error()})
	}

	return json.Marshal(mcpListToolsResponse{Tools: tools})
}

// --- mcp.call_tool ---

type mcpCallToolRequest struct {
	ProjectPath string                 `json:"project_path"`
	ServerName  string                 `json:"server_name"`
	ToolName    string                 `json:"tool_name"`
	Arguments   map[string]interface{} `json:"arguments"`
}

type mcpCallToolResponse struct {
	Result *mcp.ToolResult `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func handleMCPCallTool(_ context.Context, payload []byte) ([]byte, error) {
	if mcpMgr == nil {
		return json.Marshal(mcpCallToolResponse{Error: "MCP manager not initialized"})
	}

	var req mcpCallToolRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.ServerName == "" || req.ToolName == "" {
		return json.Marshal(mcpCallToolResponse{Error: "server_name and tool_name are required"})
	}

	var (
		result *mcp.ToolResult
		err    error
	)
	if req.ProjectPath != "" {
		result, err = mcpMgr.ProjectCallTool(req.ProjectPath, req.ServerName, req.ToolName, req.Arguments)
	} else {
		result, err = mcpMgr.CallTool(req.ServerName, req.ToolName, req.Arguments)
	}
	if err != nil {
		return json.Marshal(mcpCallToolResponse{Error: err.Error()})
	}
	return json.Marshal(mcpCallToolResponse{Result: result})
}

// --- mcp.ensure_loaded ---

type mcpEnsureLoadedRequest struct {
	ProjectPath string `json:"project_path"`
}

type mcpEnsureLoadedResponse struct {
	LoadedServers []string          `json:"loaded_servers"`
	FailedServers []string          `json:"failed_servers"`
	Errors        map[string]string `json:"errors,omitempty"`
}

func handleMCPEnsureLoaded(ctx context.Context, payload []byte) ([]byte, error) {
	if mcpMgr == nil {
		return json.Marshal(mcpEnsureLoadedResponse{})
	}

	var req mcpEnsureLoadedRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if req.ProjectPath == "" {
		return nil, fmt.Errorf("project_path is required")
	}

	result := mcpMgr.EnsureProjectServersLoaded(ctx, req.ProjectPath)

	resp := mcpEnsureLoadedResponse{
		LoadedServers: result.LoadedServers,
		FailedServers: result.FailedServers,
	}
	if len(result.Errors) > 0 {
		resp.Errors = make(map[string]string, len(result.Errors))
		for name, err := range result.Errors {
			resp.Errors[name] = err.Error()
		}
	}

	return json.Marshal(resp)
}
