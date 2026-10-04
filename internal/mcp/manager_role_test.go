package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerRoleManagerRefusesStdio(t *testing.T) {
	spawned := false
	m := NewManager(RoleServer)
	m.clientFactory = func(string, config.MCPServer) (Client, error) {
		spawned = true
		return nil, errors.New("must not be reached")
	}

	for name, cfg := range map[string]config.MCPServer{
		"typed stdio":     {Type: config.MCPStdio, Command: "npx", Enabled: true},
		"command only":    {Command: "uvx", Enabled: true},
		"command on http": {Type: config.MCPHTTP, Command: "sh", URL: "https://x", Enabled: true},
	} {
		err := m.AddServer(context.Background(), "s", cfg)
		require.ErrorIs(t, err, ErrStdioOnServer, name)
	}
	assert.False(t, spawned, "no client may be constructed for a refused server")
	assert.Empty(t, m.GetAllClients())
}

func TestDaemonRoleManagerStillAcceptsStdio(t *testing.T) {
	m := NewManager(RoleDaemon)
	m.clientFactory = func(string, config.MCPServer) (Client, error) { return nil, errors.New("factory reached") }
	err := m.AddServer(context.Background(), "s", config.MCPServer{Type: config.MCPStdio, Command: "npx", Enabled: true})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStdioOnServer)
	assert.Contains(t, err.Error(), "factory reached")
}

func TestServerRoleHasNoFilesystemFallbackOrBuiltins(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(RoleServer)
	assert.Empty(t, m.loadProjectServersFromConfig(context.Background(), dir))

	res := m.EnsureProjectServersLoaded(context.Background(), dir)
	assert.Empty(t, res.LoadedServers)
	assert.Empty(t, res.FailedServers)

	assert.NotEmpty(t, NewManager(RoleDaemon).loadProjectServersFromConfig(context.Background(), dir), "daemon keeps its built-in servers")
}
