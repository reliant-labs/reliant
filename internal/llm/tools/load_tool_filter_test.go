// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the interaction between loadable_tools and the other gates. The
// loadable_tools semantics themselves live in load_tool_discovery_test.go.

// newLoadableCtx records a declared loadable set and returns a bound context.
func newLoadableCtx(t *testing.T, permission string, loadable []string) *rctx.ToolContext {
	t.Helper()

	chatID := "loadable-" + t.Name()
	const thread = "0"
	scopeKey := Scope(chatID, thread)

	store := GetLoadedToolsStore()
	store.Clear(scopeKey)
	store.SetPermission(scopeKey, permission)
	store.SetToolAccess(scopeKey, ResolveToolAccess([]string{ToolView}, loadable, nil))
	t.Cleanup(func() { store.Clear(scopeKey) })

	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	return rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree)
}

// TestLoadTool_LoadableAndLadderAreBothRequired pins that the two gates are AND,
// not OR. Naming a tool in loadable_tools must not promote an agent past its
// permission tier, or the list becomes a privilege-escalation path.
//
// spawn is the capability the ladder still gates, so it is what can show this.
func TestLoadTool_LoadableAndLadderAreBothRequired(t *testing.T) {
	t.Parallel()

	ctx := newLoadableCtx(t, PermissionMutating, []string{"spawn"})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: "spawn"})
	require.NoError(t, err)
	assert.True(t, resp.IsError,
		"the ladder must still apply to a tool loadable_tools allows: %s", resp.Content)
}

// TestLoadTool_MCPRespectsLoadableSet closes the documented MCP gap.
//
// loadMCPTool deliberately skips the permission ladder — "MCP tools are gated by
// MCP configuration, not the agent permission ladder" — which left availability
// as the only check. A workflow that wants to bound what its agent can reach
// needs that bound to cover MCP too; the ladder exemption stays.
func TestLoadTool_MCPRespectsLoadableSet(t *testing.T) {
	t.Parallel()

	const mcpName = "mcp__chrome-devtools__take_screenshot"

	ctx := newLoadableCtx(t, PermissionMutating, []string{ToolEdit})
	GetLoadedToolsStore().SetAvailableMCPTools(Scope(ctx.ChatID, ctx.Thread), []MCPToolInfo{
		{Name: mcpName, Description: "Capture a screenshot"},
	})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)
	assert.True(t, resp.IsError,
		"a connected MCP tool outside a declared loadable set must be refused: %s", resp.Content)
}

// TestLoadTool_MCPLoadableByNameLoads — an author who wants MCP tools says so,
// and then gets them.
func TestLoadTool_MCPLoadableByNameLoads(t *testing.T) {
	t.Parallel()

	const mcpName = "mcp__chrome-devtools__take_screenshot"

	ctx := newLoadableCtx(t, PermissionMutating, []string{mcpName})
	GetLoadedToolsStore().SetAvailableMCPTools(Scope(ctx.ChatID, ctx.Thread), []MCPToolInfo{
		{Name: mcpName, Description: "Capture a screenshot"},
	})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "an MCP tool named in loadable_tools must load: %s", resp.Content)
}

// TestLoadTool_MCPUnrestrictedLoads guards the default for MCP specifically:
// the common workflow declares no loadable_tools, and connected MCP tools have
// always been reachable that way.
func TestLoadTool_MCPUnrestrictedLoads(t *testing.T) {
	t.Parallel()

	const mcpName = "mcp__chrome-devtools__take_screenshot"

	chatID := "mcp-unrestricted-" + t.Name()
	const thread = "0"
	scopeKey := Scope(chatID, thread)

	store := GetLoadedToolsStore()
	store.Clear(scopeKey)
	store.SetPermission(scopeKey, PermissionMutating)
	// "Unrestricted" is now something a workflow SAYS rather than something it
	// gets by omission — the wildcard is how the shipped workflows spell it.
	store.SetToolAccess(scopeKey, ResolveToolAccess([]string{ToolView}, []string{LoadableWildcard}, nil))
	store.SetAvailableMCPTools(scopeKey, []MCPToolInfo{
		{Name: mcpName, Description: "Capture a screenshot"},
	})
	t.Cleanup(func() { store.Clear(scopeKey) })

	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree)

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)
	assert.False(t, resp.IsError,
		"an undeclared loadable set must not restrict MCP: %s", resp.Content)
}

// TestLoadTool_SearchOmitsUnloadableTools — search must agree with what
// load_tool will actually do, or the model chases refusals.
func TestLoadTool_SearchOmitsUnloadableTools(t *testing.T) {
	t.Parallel()

	ctx := newLoadableCtx(t, PermissionMutating, []string{ToolEdit})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Query: "write"})
	require.NoError(t, err)

	assert.NotContains(t, resp.Content, "**"+ToolWrite+"**",
		"search must not advertise a tool the loadable set excludes: %s", resp.Content)
}
