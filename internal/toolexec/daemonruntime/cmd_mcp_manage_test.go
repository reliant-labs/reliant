package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/mcp"
)

// A restart that asks for the project's current config starts what the
// machine's files say now, not the server's synced copy: here the files have
// just disabled the server the copy still lists as enabled, and nothing is
// started.
func TestManageServer_PreferProjectConfigReadsTheMachinesFiles(t *testing.T) {
	config.UseUserConfigDirForTest(t, t.TempDir())
	m := mcp.NewManager(mcp.RoleDaemon)
	t.Cleanup(func() { _ = m.Close() })
	m.SetProjectConfigResolver(func(context.Context, string) (*config.Config, error) {
		return &config.Config{MCPServers: map[string]config.MCPServer{
			"tickets": {Type: config.MCPStdio, Command: "python3", Enabled: false},
		}}, nil
	})
	prev := mcpMgr()
	SetMCPManager(m)
	t.Cleanup(func() { SetMCPManager(prev) })

	stale := config.MCPServer{Type: config.MCPStdio, Command: "/deleted/old-command", Enabled: true}
	payload, err := json.Marshal(mcpManageServerRequest{
		Action:              "add",
		ProjectPath:         t.TempDir(),
		ServerName:          "tickets",
		Config:              &stale,
		PreferProjectConfig: true,
	})
	require.NoError(t, err)

	out, err := handleMCPManageServer(context.Background(), payload)
	require.NoError(t, err)
	var resp mcpManageServerResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "disabled in this project's MCP config")
	_, running := m.GetClient("tickets")
	require.False(t, running, "the stale command from the synced copy must not be started")
}
