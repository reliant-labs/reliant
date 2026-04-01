package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
)

type fakeManagerClient struct {
	closed bool
}

func (f *fakeManagerClient) Initialize(ctx context.Context) error {
	_ = ctx
	return nil
}

func (f *fakeManagerClient) ListTools() ([]Tool, error) {
	return nil, nil
}

func (f *fakeManagerClient) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	_ = name
	_ = arguments
	return &ToolResult{}, nil
}

func (f *fakeManagerClient) ListResources() ([]Resource, error) {
	return nil, nil
}

func (f *fakeManagerClient) ReadResource(uri string) (*ResourceContent, error) {
	_ = uri
	return nil, nil
}

func (f *fakeManagerClient) ListPrompts() ([]Prompt, error) {
	return nil, nil
}

func (f *fakeManagerClient) GetPrompt(name string, arguments map[string]interface{}) (*PromptResult, error) {
	_ = name
	_ = arguments
	return nil, nil
}

func (f *fakeManagerClient) Close() error {
	f.closed = true
	return nil
}

func (f *fakeManagerClient) IsConnected() bool {
	return true
}

func (f *fakeManagerClient) ServerInfo() *ServerInfo {
	return &ServerInfo{}
}

func TestEnsureProjectServersLoaded_DisabledServerNotLoadedOrFailed(t *testing.T) {
	t.Setenv("RELIANT_USER_CONFIG_DIR", t.TempDir())

	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	manager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		_ = ctx
		_ = projectPath
		return &config.Config{
			MCPServers: map[string]config.MCPServer{
				"disabled-server": {
					Type:    config.MCPStdio,
					Command: "definitely-not-a-real-command",
					Enabled: false,
				},
			},
		}, nil
	})

	result := manager.EnsureProjectServersLoaded(context.Background(), t.TempDir())
	if result == nil {
		t.Fatalf("expected non-nil load result")
	}
	// disabled-server must not be loaded; built-in servers may or may not succeed
	for _, name := range result.LoadedServers {
		if name == "disabled-server" {
			t.Fatalf("disabled server should not appear in loaded servers")
		}
	}
	for _, name := range result.FailedServers {
		if name == "disabled-server" {
			t.Fatalf("disabled server should not appear in failed servers")
		}
	}
	if _, exists := manager.GetClient("disabled-server"); exists {
		t.Fatalf("disabled server should not be present in running clients")
	}
}

func TestEnsureProjectServersLoaded_DisabledServerStopsRunningClient(t *testing.T) {
	t.Setenv("RELIANT_USER_CONFIG_DIR", t.TempDir())

	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	fakeClient := &fakeManagerClient{}
	projectPath := t.TempDir()
	manager.mu.Lock()
	manager.clients["server-a"] = fakeClient
	manager.projectServers[normalizeProjectPath(projectPath)] = map[string]bool{"server-a": true}
	manager.healthChecks["server-a"] = &healthStatus{healthy: true}
	manager.mu.Unlock()

	manager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		_ = ctx
		_ = projectPath
		return &config.Config{
			MCPServers: map[string]config.MCPServer{
				"server-a": {
					Enabled: false,
				},
			},
		}, nil
	})

	result := manager.EnsureProjectServersLoaded(context.Background(), projectPath)
	if result == nil {
		t.Fatalf("expected non-nil load result")
	}
	if result.HasFailures() {
		t.Fatalf("expected no failures when disabling running server, got failed=%v errors=%v", result.FailedServers, result.Errors)
	}

	if !fakeClient.closed {
		t.Fatalf("expected running client to be closed when server is disabled")
	}
	if _, exists := manager.GetProjectClient(projectPath, "server-a"); exists {
		t.Fatalf("expected disabled running client to be removed from manager")
	}
}

func TestGetProjectLastError_ReturnsStartupError(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	projectPath := t.TempDir()
	serverName := "failing-server"

	expectedErr := fmt.Errorf("startup failed")
	manager.healthMu.Lock()
	manager.healthChecks[serverName] = &healthStatus{healthy: false, lastError: expectedErr}
	manager.healthMu.Unlock()

	if got := manager.GetProjectLastError(projectPath, serverName); got == nil || got.Error() != expectedErr.Error() {
		t.Fatalf("expected startup error %q, got %v", expectedErr.Error(), got)
	}
}

func TestEnsureProjectServersLoaded_AssociatesAlreadyRunningServerWithNewScope(t *testing.T) {
	t.Setenv("RELIANT_USER_CONFIG_DIR", t.TempDir())

	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	fakeClient := &fakeManagerClient{}
	projectPathA := t.TempDir()
	projectPathB := t.TempDir()

	manager.mu.Lock()
	manager.clients["server-a"] = fakeClient
	manager.projectServers[normalizeProjectPath(projectPathA)] = map[string]bool{"server-a": true}
	manager.healthChecks["server-a"] = &healthStatus{healthy: true}
	manager.mu.Unlock()

	manager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		_ = ctx
		return &config.Config{
			MCPServers: map[string]config.MCPServer{
				"server-a": {
					Enabled: true,
				},
			},
		}, nil
	})

	result := manager.EnsureProjectServersLoaded(context.Background(), projectPathB)
	if result == nil {
		t.Fatalf("expected non-nil load result")
	}
	// server-a must be in loaded servers; built-in servers may also appear
	foundServerA := false
	for _, name := range result.LoadedServers {
		if name == "server-a" {
			foundServerA = true
		}
	}
	if !foundServerA {
		t.Fatalf("expected already-running server-a to be reported as loaded, got %v", result.LoadedServers)
	}
	if fakeClient.closed {
		t.Fatalf("expected already-running client to remain active")
	}
	if _, exists := manager.GetProjectClient(projectPathB, "server-a"); !exists {
		t.Fatalf("expected already-running client to be associated with second scope")
	}
	if _, exists := manager.GetProjectClient(projectPathA, "server-a"); !exists {
		t.Fatalf("expected original scope association to remain intact")
	}
	clients := manager.GetProjectClients(projectPathB)
	if _, ok := clients["server-a"]; !ok {
		t.Fatalf("expected server-a in clients for second scope, got %v", clients)
	}
}

func TestLoadProjectServersFromConfig_MergesFilesystemScopeOverridesOverResolvedConfig(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	userConfigDir := t.TempDir()
	t.Setenv("RELIANT_USER_CONFIG_DIR", userConfigDir)

	projectPath := t.TempDir()
	projectLocalDir := filepath.Join(projectPath, config.ReliantLocalDir)
	if err := os.MkdirAll(projectLocalDir, 0o755); err != nil {
		t.Fatalf("failed to create project-local config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectLocalDir, config.MCPConfigFileName), []byte(`{
  "mcpServers": {
    "shared": {"command": "worktree-cmd", "type": "stdio"},
    "worktree-only": {"command": "worktree-only-cmd", "type": "stdio"}
  }
}`), 0o644); err != nil {
		t.Fatalf("failed to write worktree mcp config: %v", err)
	}

	manager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		_ = ctx
		_ = projectPath
		return &config.Config{
			MCPServers: map[string]config.MCPServer{
				"shared": {
					Command: "project-cmd",
					Type:    config.MCPStdio,
					Enabled: true,
				},
				"project-only": {
					Command: "project-only-cmd",
					Type:    config.MCPStdio,
					Enabled: true,
				},
			},
		}, nil
	})

	servers := manager.loadProjectServersFromConfig(context.Background(), projectPath)
	builtinCount := len(builtinMCPServers())
	if len(servers) != 3+builtinCount {
		t.Fatalf("expected merged servers from provider + filesystem + builtin, got %d: %#v", len(servers), servers)
	}
	if got := servers["shared"].Command; got != "worktree-cmd" {
		t.Fatalf("expected filesystem worktree override for shared server, got %q", got)
	}
	if got := servers["project-only"].Command; got != "project-only-cmd" {
		t.Fatalf("expected provider-backed project server to remain present, got %q", got)
	}
	if got := servers["worktree-only"].Command; got != "worktree-only-cmd" {
		t.Fatalf("expected worktree-only filesystem server to be present, got %q", got)
	}
}

func TestLoadProjectServersFromConfig_FallsBackToFilesystemWhenResolverFails(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	userConfigDir := t.TempDir()
	t.Setenv("RELIANT_USER_CONFIG_DIR", userConfigDir)

	projectPath := t.TempDir()
	projectDir := filepath.Join(projectPath, config.ReliantDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("failed to create project config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, config.MCPConfigFileName), []byte(`{
  "mcpServers": {
    "filesystem-only": {"command": "filesystem-cmd", "type": "stdio"}
  }
}`), 0o644); err != nil {
		t.Fatalf("failed to write project mcp config: %v", err)
	}

	manager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		_ = ctx
		_ = projectPath
		return nil, fmt.Errorf("resolver failed")
	})

	servers := manager.loadProjectServersFromConfig(context.Background(), projectPath)
	builtinCount := len(builtinMCPServers())
	if len(servers) != 1+builtinCount {
		t.Fatalf("expected filesystem fallback + builtin servers, got %d: %#v", len(servers), servers)
	}
	if got := servers["filesystem-only"].Command; got != "filesystem-cmd" {
		t.Fatalf("expected filesystem fallback server command, got %q", got)
	}
}

func TestLoadProjectServersFromConfig_IncludesBuiltinServers(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	userConfigDir := t.TempDir()
	t.Setenv("RELIANT_USER_CONFIG_DIR", userConfigDir)

	projectPath := t.TempDir()

	// No resolver, no filesystem config — should still get built-in servers.
	servers := manager.loadProjectServersFromConfig(context.Background(), projectPath)
	builtin := builtinMCPServers()
	if len(servers) != len(builtin) {
		t.Fatalf("expected only built-in servers with no user config, got %d: %#v", len(servers), servers)
	}
	for name, expected := range builtin {
		got, ok := servers[name]
		if !ok {
			t.Fatalf("expected built-in server %q to be present", name)
		}
		if got.URL != expected.URL {
			t.Fatalf("expected built-in server %q URL %q, got %q", name, expected.URL, got.URL)
		}
	}
}

func TestLoadProjectServersFromConfig_UserConfigOverridesBuiltin(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() {
		_ = manager.Close()
	})

	userConfigDir := t.TempDir()
	t.Setenv("RELIANT_USER_CONFIG_DIR", userConfigDir)

	projectPath := t.TempDir()
	projectDir := filepath.Join(projectPath, config.ReliantDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("failed to create project config dir: %v", err)
	}
	// User overrides the built-in reliant-docs server with a custom URL.
	if err := os.WriteFile(filepath.Join(projectDir, config.MCPConfigFileName), []byte(`{
  "mcpServers": {
    "reliant-docs": {"type": "http", "url": "https://custom-docs.example.com/mcp"}
  }
}`), 0o644); err != nil {
		t.Fatalf("failed to write project mcp config: %v", err)
	}

	servers := manager.loadProjectServersFromConfig(context.Background(), projectPath)
	got, ok := servers["reliant-docs"]
	if !ok {
		t.Fatalf("expected reliant-docs server to be present")
	}
	if got.URL != "https://custom-docs.example.com/mcp" {
		t.Fatalf("expected user config to override built-in URL, got %q", got.URL)
	}
}
