package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

func TestMCPService_SetServerEnabled_UpdatesStatusAcrossToggles(t *testing.T) {
	ctx, svc, _, projectID := setupMCPServiceToggleTest(t)
	seedMCPServerConfig(t, svc, ctx, projectID, true)

	server := getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.True(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED, server.Status)

	disableResp, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   false,
	}))
	require.NoError(t, err)
	require.True(t, disableResp.Msg.Success)
	require.False(t, disableResp.Msg.Enabled)

	server = getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.False(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED, server.Status)
	require.False(t, server.Connected)
	require.False(t, server.Healthy)

	disabledGetServer, err := svc.GetServer(ctx, connect.NewRequest(&reliantv1.GetServerRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.False(t, disabledGetServer.Msg.Server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED, disabledGetServer.Msg.Server.Status)

	disabledTools, err := svc.GetServerTools(ctx, connect.NewRequest(&reliantv1.GetServerToolsRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 0, disabledTools.Msg.Total)

	enableResp, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   true,
	}))
	require.NoError(t, err)
	require.False(t, enableResp.Msg.Success, "go version is not an MCP server; startup should fail in this test")
	require.True(t, enableResp.Msg.Enabled)

	server = getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.True(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED, server.Status, "re-enabled server should no longer report disabled status")
	require.False(t, server.Connected)
	require.False(t, server.Healthy)
	require.NotNil(t, server.LastError)
	require.NotEmpty(t, *server.LastError)

	enabledGetServer, err := svc.GetServer(ctx, connect.NewRequest(&reliantv1.GetServerRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.True(t, enabledGetServer.Msg.Server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED, enabledGetServer.Msg.Server.Status)

	reEnabledTools, err := svc.GetServerTools(ctx, connect.NewRequest(&reliantv1.GetServerToolsRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 0, reEnabledTools.Msg.Total)

	disableAgainResp, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   false,
	}))
	require.NoError(t, err)
	require.True(t, disableAgainResp.Msg.Success)
	require.False(t, disableAgainResp.Msg.Enabled)

	server = getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.False(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED, server.Status)
}

func setupMCPServiceToggleTest(t *testing.T) (context.Context, *MCPService, *fakeMCPManagerRuntime, string) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	t.Setenv("RELIANT_USER_CONFIG_DIR", filepath.Join(t.TempDir(), "user-config"))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	manager := newFakeMCPManagerRuntime()
	router := newFakeMCPDaemonRouter(manager)

	svc := NewMCPService(repo, router)

	projectPath := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(projectPath, 0o755))

	projectID := "test-project-mcp-toggle-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "MCP Toggle Test",
		Path:       projectPath,
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	return ctx, svc, manager, projectID
}

func TestMCPService_SetServerEnabled_EnableStatusBecomesHealthyWhenManagerHasClient(t *testing.T) {
	ctx, svc, manager, projectID := setupMCPServiceToggleTest(t)
	seedMCPServerConfig(t, svc, ctx, projectID, false)

	server := getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.False(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED, server.Status)

	manager.startErr = nil
	manager.addClient = &fakeManagerClient{
		connected: true,
		tools: []mcp.Tool{
			{Name: "tool-a", Description: "A tool"},
		},
		info: &mcp.ServerInfo{
			Name:    "fake-server",
			Version: "1.0.0",
			Capabilities: mcp.ServerCapabilities{
				Tools: &mcp.ToolsCapability{},
			},
		},
	}
	manager.healthByName["toggle-server"] = true
	delete(manager.lastErrByName, "toggle-server")

	enableResp, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   true,
	}))
	require.NoError(t, err)
	require.True(t, enableResp.Msg.Success)
	require.True(t, enableResp.Msg.Enabled)

	server = getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.True(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, server.Status)
	require.True(t, server.Connected)
	require.True(t, server.Healthy)
	require.EqualValues(t, 1, server.ToolCount)
	require.Nil(t, server.LastError)

	getServerResp, err := svc.GetServer(ctx, connect.NewRequest(&reliantv1.GetServerRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, getServerResp.Msg.Server.Status)
	require.True(t, getServerResp.Msg.Server.Connected)
	require.True(t, getServerResp.Msg.Server.Healthy)
	require.EqualValues(t, 1, getServerResp.Msg.Server.ToolCount)

	toolsResp, err := svc.GetServerTools(ctx, connect.NewRequest(&reliantv1.GetServerToolsRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, toolsResp.Msg.Total)
	require.Equal(t, "tool-a", toolsResp.Msg.Tools[0].Name)
}

func TestMCPService_SetServerEnabled_SuccessfulReenableClearsLastError(t *testing.T) {
	ctx, svc, manager, projectID := setupMCPServiceToggleTest(t)
	seedMCPServerConfig(t, svc, ctx, projectID, false)

	failedEnableResp, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   true,
	}))
	require.NoError(t, err)
	require.False(t, failedEnableResp.Msg.Success)
	require.True(t, failedEnableResp.Msg.Enabled)

	server := getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.True(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED, server.Status)
	require.NotNil(t, server.LastError)
	require.NotEmpty(t, *server.LastError)

	manager.startErr = nil
	manager.addClient = &fakeManagerClient{
		connected: true,
		tools:     []mcp.Tool{{Name: "tool-a", Description: "A tool"}},
		info: &mcp.ServerInfo{
			Name:         "fake-server",
			Version:      "1.0.0",
			Capabilities: mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}},
		},
	}

	successEnableResp, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   true,
	}))
	require.NoError(t, err)
	require.True(t, successEnableResp.Msg.Success)
	require.True(t, successEnableResp.Msg.Enabled)

	server = getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.True(t, server.Enabled)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, server.Status)
	require.True(t, server.Connected)
	require.True(t, server.Healthy)
	require.Nil(t, server.LastError, "successful re-enable should clear previous startup error")

	getServerResp, err := svc.GetServer(ctx, connect.NewRequest(&reliantv1.GetServerRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, getServerResp.Msg.Server.Status)
	require.Nil(t, getServerResp.Msg.Server.LastError)
}

func TestMCPService_ListServers_PrefersStoredConfigSnapshotOverFilesystem(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	t.Setenv("RELIANT_USER_CONFIG_DIR", filepath.Join(t.TempDir(), "user-config"))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	projectPath := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(filepath.Join(projectPath, ".reliant"), 0o755))

	projectID := "test-project-stored-mcp-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Stored MCP Test",
		Path:       projectPath,
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	// Local filesystem says enabled=true.
	mcpPath := filepath.Join(projectPath, ".reliant", config.MCPConfigFileName)
	cfgJSON := `{"mcpServers":{"toggle-server":{"command":"go","args":["version"],"enabled":true}}}`
	require.NoError(t, os.WriteFile(mcpPath, []byte(cfgJSON), 0o644))

	// Stored daemon snapshot is source-of-truth and says enabled=false.
	storedMCP := `{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":false}}}"}`
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))

	svc := NewMCPService(repo, newFakeMCPDaemonRouter(newFakeMCPManagerRuntime()))

	server := getServerFromList(t, svc, ctx, projectID, "toggle-server")
	require.False(t, server.Enabled, "stored config should override filesystem fallback")
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED, server.Status)
}

func TestMCPService_SetServerEnabled_UpdatesStoredSnapshot(t *testing.T) {
	ctx, svc, _, projectID := setupMCPServiceToggleTest(t)
	seedMCPServerConfig(t, svc, ctx, projectID, true)

	// Seed stored snapshot with enabled=true so we can verify write-through update.
	now := time.Now().UTC()
	storedMCP := `{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))

	_, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Enabled:   false,
	}))
	require.NoError(t, err)

	record, err := svc.database.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotNil(t, record.MCPConfigs)

	storedServers := readMergedScopedServersFromStoredRecord(record)
	require.Contains(t, storedServers, "toggle-server")
	require.False(t, storedServers["toggle-server"].Config.IsEnabled(), "stored snapshot should be updated by SetServerEnabled")
}

func TestMCPService_UpdateServerConfig_UpdatesStoredSnapshot(t *testing.T) {
	ctx, svc, manager, projectID := setupMCPServiceToggleTest(t)
	manager.startErr = nil
	seedMCPServerConfig(t, svc, ctx, projectID, true)

	now := time.Now().UTC()
	storedMCP := `{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))

	_, err := svc.UpdateServerConfig(ctx, connect.NewRequest(&reliantv1.UpdateServerConfigRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Env: map[string]string{
			"TOKEN": "abc123",
		},
	}))
	require.NoError(t, err)

	record, err := svc.database.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	storedServers := readMergedScopedServersFromStoredRecord(record)
	require.Contains(t, storedServers, "toggle-server")
	require.Equal(t, "abc123", storedServers["toggle-server"].Config.Env["TOKEN"])
}

func TestMCPService_InstallServer_PersistsToStoredSnapshot(t *testing.T) {
	ctx, svc, manager, projectID := setupMCPServiceToggleTest(t)
	manager.startErr = nil

	now := time.Now().UTC()
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID,
		DaemonID:  "daemon-test",
		PushedAt:  now,
	}))

	resp, err := svc.InstallServer(ctx, connect.NewRequest(&reliantv1.InstallServerRequest{
		ProjectId: projectID,
		Name:      "installed-server",
		Scope:     reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
		Config: &reliantv1.MCPServerConfig{
			Command: "go",
			Args:    []string{"version"},
		},
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	record, err := svc.database.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	storedServers := readMergedScopedServersFromStoredRecord(record)
	require.Contains(t, storedServers, "installed-server")
	require.True(t, storedServers["installed-server"].Config.IsEnabled())
}

func TestMCPService_MoveServerScope_UsesStoredSnapshotAndPersistsMove(t *testing.T) {
	ctx, svc, manager, projectID := setupMCPServiceToggleTest(t)
	manager.startErr = nil

	// Stored snapshot has server in project scope and empty local scope.
	now := time.Now().UTC()
	storedMCP := `{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}","local":"{\"mcpServers\":{}}"}`
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))

	resp, err := svc.MoveServerScope(ctx, connect.NewRequest(&reliantv1.MoveServerScopeRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
		Scope:     reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	require.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL, resp.Msg.Scope)

	record, err := svc.database.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.NotNil(t, record)
	servers := readMergedScopedServersFromStoredRecord(record)
	require.Contains(t, servers, "toggle-server")
	require.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL, servers["toggle-server"].Scope)
}

func TestMCPService_UninstallServer_RemovesFromStoredSnapshot(t *testing.T) {
	ctx, svc, _, projectID := setupMCPServiceToggleTest(t)
	seedMCPServerConfig(t, svc, ctx, projectID, true)

	now := time.Now().UTC()
	storedMCP := `{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))

	resp, err := svc.UninstallServer(ctx, connect.NewRequest(&reliantv1.UninstallServerRequest{
		ProjectId: projectID,
		Name:      "toggle-server",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	record, err := svc.database.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.NotNil(t, record)
	servers := readMergedScopedServersFromStoredRecord(record)
	require.NotContains(t, servers, "toggle-server")
}

func TestMCPService_ReadMergedScopedServers_GlobalScopeUsesStoredRecord(t *testing.T) {
	ctx, svc, _, projectID := setupMCPServiceToggleTest(t)

	now := time.Now().UTC()
	storedGlobal := `{"user":"{\"mcpServers\":{\"shared-global\":{\"command\":\"global-cmd\",\"args\":[\"--from-db\"],\"enabled\":true}}}"}`
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedGlobal,
		PushedAt:   now,
	}))

	servers, err := svc.readMergedScopedServers(ctx, projectID)
	require.NoError(t, err)
	require.Contains(t, servers, "shared-global")
	require.Equal(t, "global-cmd", servers["shared-global"].Config.Command)
	require.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, servers["shared-global"].Scope)
}

func TestMCPService_SetServerEnabled_GlobalScopePropagatesStoredSnapshotAcrossProjects(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	t.Setenv("RELIANT_USER_CONFIG_DIR", filepath.Join(t.TempDir(), "user-config"))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	projectPathA := filepath.Join(t.TempDir(), "project-a")
	projectPathB := filepath.Join(t.TempDir(), "project-b")
	require.NoError(t, os.MkdirAll(projectPathA, 0o755))
	require.NoError(t, os.MkdirAll(projectPathB, 0o755))

	projectA := "test-project-a-" + uuid.NewString()
	projectB := "test-project-b-" + uuid.NewString()
	now := time.Now().UTC()

	for _, project := range []*db.Project{
		{ID: projectA, UserID: "test-user", Name: "A", Path: projectPathA, IsGitRepo: false, CreatedAt: now, UpdatedAt: now, LastActive: now},
		{ID: projectB, UserID: "test-user", Name: "B", Path: projectPathB, IsGitRepo: false, CreatedAt: now, UpdatedAt: now, LastActive: now},
	} {
		require.NoError(t, repo.CreateProject(ctx, project))
	}

	sharedGlobalEnabled := `{"user":"{\"mcpServers\":{\"shared-global\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectA,
		DaemonID:   "daemon-a",
		MCPConfigs: &sharedGlobalEnabled,
		PushedAt:   now,
	}))
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectB,
		DaemonID:   "daemon-b",
		MCPConfigs: &sharedGlobalEnabled,
		PushedAt:   now,
	}))

	globalPath := filepath.Join(config.GetUserConfigDir(), config.MCPConfigFileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o755))
	require.NoError(t, os.WriteFile(globalPath, []byte(`{"mcpServers":{"shared-global":{"command":"go","args":["version"],"enabled":true}}}`), 0o644))

	svc := NewMCPService(repo, newFakeMCPDaemonRouter(newFakeMCPManagerRuntime()))
	_, err := svc.SetServerEnabled(ctx, connect.NewRequest(&reliantv1.SetServerEnabledRequest{
		ProjectId: projectA,
		Name:      "shared-global",
		Enabled:   false,
	}))
	require.NoError(t, err)

	recordA, err := repo.GetProjectConfigRecord(ctx, projectA)
	require.NoError(t, err)
	recordB, err := repo.GetProjectConfigRecord(ctx, projectB)
	require.NoError(t, err)

	serversA := readMergedScopedServersFromStoredRecord(recordA)
	serversB := readMergedScopedServersFromStoredRecord(recordB)
	require.Contains(t, serversA, "shared-global")
	require.Contains(t, serversB, "shared-global")
	require.False(t, serversA["shared-global"].Config.IsEnabled())
	require.False(t, serversB["shared-global"].Config.IsEnabled())
}

func seedMCPServerConfig(t *testing.T, svc *MCPService, ctx context.Context, projectID string, enabled bool) {
	t.Helper()

	enabledStr := "true"
	if !enabled {
		enabledStr = "false"
	}
	storedMCP := fmt.Sprintf(`{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":%s}}}"}`, enabledStr)
	now := time.Now().UTC()
	require.NoError(t, svc.database.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))
}

type fakeManagerClient struct {
	connected bool
	tools     []mcp.Tool
	info      *mcp.ServerInfo
}

func (c *fakeManagerClient) Initialize(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *fakeManagerClient) ListTools() ([]mcp.Tool, error) {
	return c.tools, nil
}

func (c *fakeManagerClient) CallTool(name string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	_ = name
	_ = arguments
	return &mcp.ToolResult{}, nil
}

func (c *fakeManagerClient) ListResources() ([]mcp.Resource, error) {
	return nil, nil
}

func (c *fakeManagerClient) ReadResource(uri string) (*mcp.ResourceContent, error) {
	_ = uri
	return nil, nil
}

func (c *fakeManagerClient) ListPrompts() ([]mcp.Prompt, error) {
	return nil, nil
}

func (c *fakeManagerClient) GetPrompt(name string, arguments map[string]interface{}) (*mcp.PromptResult, error) {
	_ = name
	_ = arguments
	return nil, nil
}

func (c *fakeManagerClient) Close() error {
	c.connected = false
	return nil
}

func (c *fakeManagerClient) IsConnected() bool {
	return c.connected
}

func (c *fakeManagerClient) ServerInfo() *mcp.ServerInfo {
	if c.info != nil {
		return c.info
	}
	return &mcp.ServerInfo{Capabilities: mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}}}
}

type fakeMCPManagerRuntime struct {
	clientByName   map[string]mcp.Client
	healthByName   map[string]bool
	lastErrByName  map[string]error
	projectServers map[string]map[string]bool // projectPath -> set of server names
	startErr       error
	addClient      mcp.Client
}

func newFakeMCPManagerRuntime() *fakeMCPManagerRuntime {
	return &fakeMCPManagerRuntime{
		clientByName:   make(map[string]mcp.Client),
		healthByName:   make(map[string]bool),
		lastErrByName:  make(map[string]error),
		projectServers: make(map[string]map[string]bool),
		startErr:       fmt.Errorf("fake start failure"),
	}
}

func (m *fakeMCPManagerRuntime) AddServer(ctx context.Context, name string, cfg config.MCPServer) error {
	_ = ctx
	_ = cfg
	if m.startErr != nil {
		m.healthByName[name] = false
		m.lastErrByName[name] = m.startErr
		delete(m.clientByName, name)
		return m.startErr
	}
	if m.addClient != nil {
		m.clientByName[name] = m.addClient
	} else if _, exists := m.clientByName[name]; !exists {
		m.clientByName[name] = &fakeManagerClient{connected: true, info: &mcp.ServerInfo{Capabilities: mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}}}}
	}
	m.healthByName[name] = true
	delete(m.lastErrByName, name)
	return nil
}

func (m *fakeMCPManagerRuntime) RemoveServer(name string) error {
	if client, exists := m.clientByName[name]; exists {
		_ = client.Close()
		delete(m.clientByName, name)
		delete(m.healthByName, name)
		delete(m.lastErrByName, name)
		// Remove from all project sets
		for projectPath, servers := range m.projectServers {
			delete(servers, name)
			if len(servers) == 0 {
				delete(m.projectServers, projectPath)
			}
		}
		return nil
	}
	delete(m.healthByName, name)
	delete(m.lastErrByName, name)
	return fmt.Errorf("server %s not found", name)
}

func (m *fakeMCPManagerRuntime) AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	normalizedProjectPath := filepath.Clean(projectPath)

	if err := m.AddServer(ctx, serverName, cfg); err != nil {
		return err
	}

	servers := m.projectServers[normalizedProjectPath]
	if servers == nil {
		servers = make(map[string]bool)
		m.projectServers[normalizedProjectPath] = servers
	}
	servers[serverName] = true
	return nil
}

func (m *fakeMCPManagerRuntime) RemoveProjectServer(projectPath, serverName string) error {
	normalizedProjectPath := filepath.Clean(projectPath)
	if servers, ok := m.projectServers[normalizedProjectPath]; ok {
		delete(servers, serverName)
		if len(servers) == 0 {
			delete(m.projectServers, normalizedProjectPath)
		}
	}
	return m.RemoveServer(serverName)
}

func (m *fakeMCPManagerRuntime) RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	_ = m.RemoveProjectServer(projectPath, serverName)
	return m.AddProjectServer(ctx, projectPath, serverName, cfg)
}

func (m *fakeMCPManagerRuntime) GetProjectClients(projectPath string) map[string]mcp.Client {
	normalizedProjectPath := filepath.Clean(projectPath)
	result := make(map[string]mcp.Client)
	for name := range m.projectServers[normalizedProjectPath] {
		if client, exists := m.clientByName[name]; exists {
			result[name] = client
		}
	}
	return result
}

func (m *fakeMCPManagerRuntime) GetProjectHealthStatus(projectPath string) map[string]bool {
	normalizedProjectPath := filepath.Clean(projectPath)
	result := make(map[string]bool)
	for name := range m.projectServers[normalizedProjectPath] {
		if healthy, exists := m.healthByName[name]; exists {
			result[name] = healthy
		}
	}
	return result
}

func (m *fakeMCPManagerRuntime) GetProjectLastError(_, serverName string) error {
	return m.lastErrByName[serverName]
}

func (m *fakeMCPManagerRuntime) GetProjectClient(projectPath, serverName string) (mcp.Client, bool) {
	normalizedProjectPath := filepath.Clean(projectPath)
	if servers, ok := m.projectServers[normalizedProjectPath]; ok && servers[serverName] {
		client, exists := m.clientByName[serverName]
		return client, exists
	}
	return nil, false
}

func (m *fakeMCPManagerRuntime) GetAllClients() map[string]mcp.Client {
	copyMap := make(map[string]mcp.Client, len(m.clientByName))
	for k, v := range m.clientByName {
		copyMap[k] = v
	}
	return copyMap
}

func (m *fakeMCPManagerRuntime) GetHealthStatus() map[string]bool {
	copyMap := make(map[string]bool, len(m.healthByName))
	for k, v := range m.healthByName {
		copyMap[k] = v
	}
	return copyMap
}

func (m *fakeMCPManagerRuntime) GetLastError(name string) error {
	return m.lastErrByName[name]
}

func (m *fakeMCPManagerRuntime) GetClient(name string) (mcp.Client, bool) {
	client, ok := m.clientByName[name]
	return client, ok
}

// fakeMCPDaemonRouter implements toolexec.DaemonRouter by delegating MCP daemon
// commands to a fakeMCPManagerRuntime. All non-MCP methods are no-ops.
type fakeMCPDaemonRouter struct {
	mgr       *fakeMCPManagerRuntime
	attempted map[string]map[string]bool // projectPath -> set of server names attempted
}

func newFakeMCPDaemonRouter(mgr *fakeMCPManagerRuntime) *fakeMCPDaemonRouter {
	return &fakeMCPDaemonRouter{
		mgr:       mgr,
		attempted: make(map[string]map[string]bool),
	}
}

func (r *fakeMCPDaemonRouter) IsDaemonOnline(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (r *fakeMCPDaemonRouter) SendToolRequest(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) error {
	return nil
}
func (r *fakeMCPDaemonRouter) SendToolExecutionCancel(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *fakeMCPDaemonRouter) SendKillProcess(_ context.Context, _, _ string) error { return nil }
func (r *fakeMCPDaemonRouter) SendLoadProjectConfigs(_ context.Context, _, _ string, _ string) error {
	return nil
}
func (r *fakeMCPDaemonRouter) SendWatchProjectConfigs(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}
func (r *fakeMCPDaemonRouter) SendToolRequestSync(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (r *fakeMCPDaemonRouter) SendToolRequestSyncWithSelector(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (r *fakeMCPDaemonRouter) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (r *fakeMCPDaemonRouter) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}
func (r *fakeMCPDaemonRouter) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *fakeMCPDaemonRouter) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	ch := make(chan *toolexec.ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *fakeMCPDaemonRouter) Close() error { return nil }

func (r *fakeMCPDaemonRouter) EnqueueDaemonCommand(_ context.Context, _ string, _ string, _ []byte, _ int32) (int, error) {
	return 0, nil
}

func (r *fakeMCPDaemonRouter) ResolveDaemonID(_ context.Context, _ string) (string, error) {
	return "test-daemon-id", nil
}

func (r *fakeMCPDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	switch commandType {
	case "mcp.server_status":
		var req struct {
			ProjectPath string `json:"project_path"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		clients := r.mgr.GetProjectClients(req.ProjectPath)
		health := r.mgr.GetProjectHealthStatus(req.ProjectPath)

		// Collect all server names: successful (from clients) + failed (from attempted)
		allNames := make(map[string]bool)
		for name := range clients {
			allNames[name] = true
		}
		for name := range r.attempted[req.ProjectPath] {
			allNames[name] = true
		}

		var status daemonMCPServerStatus
		for name := range allNames {
			entry := daemonMCPServerStatusEntry{Name: name}
			if client, ok := clients[name]; ok {
				entry.Connected = client.IsConnected()
				entry.Healthy = health[name]
				if info := client.ServerInfo(); info != nil {
					entry.ServerInfo = info
				}
				if tools, err := client.ListTools(); err == nil {
					entry.ToolCount = len(tools)
				}
			}
			if err := r.mgr.GetProjectLastError(req.ProjectPath, name); err != nil {
				entry.LastError = err.Error()
			}
			status.Servers = append(status.Servers, entry)
		}
		return json.Marshal(status)

	case "mcp.manage_server":
		var req struct {
			Action      string            `json:"action"`
			ProjectPath string            `json:"project_path"`
			ServerName  string            `json:"server_name"`
			Config      *config.MCPServer `json:"config,omitempty"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		// Track that this server was attempted for this project
		if req.Action == "add" || req.Action == "restart" {
			if r.attempted[req.ProjectPath] == nil {
				r.attempted[req.ProjectPath] = make(map[string]bool)
			}
			r.attempted[req.ProjectPath][req.ServerName] = true
		}
		var err error
		switch req.Action {
		case "add":
			err = r.mgr.AddProjectServer(context.Background(), req.ProjectPath, req.ServerName, *req.Config)
		case "remove":
			err = r.mgr.RemoveProjectServer(req.ProjectPath, req.ServerName)
		case "restart":
			err = r.mgr.RestartProjectServer(context.Background(), req.ProjectPath, req.ServerName, *req.Config)
		}
		if err != nil {
			return json.Marshal(map[string]interface{}{"success": false, "error": err.Error()})
		}
		return json.Marshal(map[string]interface{}{"success": true})

	case "mcp.list_tools":
		var req struct {
			ProjectPath string `json:"project_path"`
			ServerName  string `json:"server_name"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		client, ok := r.mgr.GetProjectClient(req.ProjectPath, req.ServerName)
		if !ok {
			return json.Marshal(map[string]interface{}{"tools": []mcp.Tool{}})
		}
		tools, err := client.ListTools()
		if err != nil {
			return json.Marshal(map[string]interface{}{"tools": []mcp.Tool{}, "error": err.Error()})
		}
		return json.Marshal(map[string]interface{}{"tools": tools})

	case "mcp.ensure_loaded", "mcp.write_config":
		return json.Marshal(map[string]interface{}{"success": true})

	default:
		return nil, fmt.Errorf("unhandled daemon command: %s", commandType)
	}
}

func getServerFromList(t *testing.T, svc *MCPService, ctx context.Context, projectID, serverName string) *reliantv1.MCPServer {
	t.Helper()

	listResp, err := svc.ListServers(ctx, connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: projectID}))
	require.NoError(t, err)

	for _, server := range listResp.Msg.Servers {
		if server.Name == serverName {
			return server
		}
	}

	t.Fatalf("server %q not found in ListServers response", serverName)
	return nil
}
