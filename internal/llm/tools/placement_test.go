package tools

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlacementOf_BuiltIns(t *testing.T) {
	for name, want := range map[string]Placement{
		ShellToolName: PlacementDaemon,
		ToolFetch:     PlacementAny,
		ToolAskUser:   PlacementServer,
	} {
		got, err := PlacementOf(name)
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
}

func TestPlacementOf_UnknownToolIsAnError(t *testing.T) {
	for _, name := range []string{"no_such_tool", "", "mcp_single_underscore"} {
		got, err := PlacementOf(name)
		require.Error(t, err, "%q must not resolve to a placement", name)
		assert.Empty(t, got)
	}
}

func TestPlacementOf_MCPToolsInheritDaemon(t *testing.T) {
	for _, name := range []string{"mcp__serena__find_symbol", "mcp__chrome-devtools__new_page", "mcp__*"} {
		got, err := PlacementOf(name)
		require.NoError(t, err, name)
		assert.Equal(t, PlacementDaemon, got, name)
	}
}

func TestMCPServerPlacement_UserConfigNeverClaimsServer(t *testing.T) {
	for _, cfg := range []config.MCPServer{
		{Type: config.MCPStdio, Command: "npx"},
		{Type: config.MCPHTTP, URL: "https://example.com/mcp"},
		{Type: config.MCPSse, URL: "http://localhost:9000/sse"},
		{},
	} {
		assert.Equal(t, PlacementDaemon, MCPServerPlacement(cfg), "%+v", cfg)
	}
}

func TestEveryRegistryToolHasAValidPlacement(t *testing.T) {
	for _, def := range GetToolRegistry() {
		assert.True(t, def.Placement.Valid(), "%s has placement %q", def.Name, def.Placement)
	}
}
