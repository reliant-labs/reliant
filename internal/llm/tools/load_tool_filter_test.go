// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFilteredTestCtx builds a tool context whose scope carries BOTH a permission
// level and a declared allow-set, which is the combination a workflow with a
// `tools:` filter produces.
func newFilteredTestCtx(t *testing.T, permission string, allowed []string) *rctx.ToolContext {
	t.Helper()
	chatID := "filter-test-" + t.Name()
	const thread = "0"
	scopeKey := Scope(chatID, thread)

	store := GetLoadedToolsStore()
	store.Clear(scopeKey)
	store.SetPermission(scopeKey, permission)
	store.SetAllowedTools(scopeKey, allowed)

	t.Cleanup(func() { store.Clear(scopeKey) })

	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	return rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree)
}

// TestLoadTool_CannotEscapeDeclaredFilter is the point of the whole change.
//
// A workflow that declares `tools: [view]` is making a statement about what its
// agent should have. Before this, load_tool consulted only the permission
// ladder — which defaults to `mutating` — so the agent could simply ask for
// `write` and get it, and the loaded tool was appended AFTER filter expansion,
// so even an explicit exclusion was overridden.
func TestLoadTool_CannotEscapeDeclaredFilter(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newFilteredTestCtx(t, PermissionMutating, []string{ToolView, ToolLoadTool})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)

	assert.True(t, resp.IsError,
		"loading a tool outside the declared filter must fail, got: %s", resp.Content)
	assert.False(t, GetLoadedToolsStore().Has(Scope(ctx.ChatID, ctx.Thread), ToolWrite),
		"a refused tool must not be recorded as loaded")
}

// TestLoadTool_AllowedByFilterStillHonoursLadder pins that the two checks are
// AND, not OR. Naming a tool in `tools:` must not promote an agent past its
// permission level — otherwise the filter becomes a privilege escalation path.
func TestLoadTool_AllowedByFilterStillHonoursLadder(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newFilteredTestCtx(t, PermissionReadOnly, []string{ToolView, ToolWrite, ToolLoadTool})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)

	assert.True(t, resp.IsError,
		"the ladder must still apply to a tool the filter allows, got: %s", resp.Content)
}

// TestLoadTool_AllowedByBothSucceeds — the positive case, so the guard cannot be
// satisfied by refusing everything.
func TestLoadTool_AllowedByBothSucceeds(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newFilteredTestCtx(t, PermissionMutating, []string{ToolView, ToolWrite, ToolLoadTool})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)

	assert.False(t, resp.IsError,
		"a tool allowed by both filter and ladder must load: %s", resp.Content)
	assert.True(t, GetLoadedToolsStore().Has(Scope(ctx.ChatID, ctx.Thread), ToolWrite))
}

// TestLoadTool_UndeclaredFilterAllowsAnything guards the compatibility case. A
// workflow with no `tools:` filter places no restriction; reading "no
// declaration" as "deny all" would break every such workflow, and would also
// strand a live run whose scope was lost to a worker restart.
func TestLoadTool_UndeclaredFilterAllowsAnything(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newFilteredTestCtx(t, PermissionMutating, nil)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)

	assert.False(t, resp.IsError,
		"an undeclared filter must not restrict anything: %s", resp.Content)
}

// TestLoadTool_MCPToolRespectsFilter closes the documented MCP bypass.
// loadMCPTool deliberately skips the permission ladder ("MCP tools are gated by
// MCP configuration, not the agent permission ladder"), which left a workflow
// with a restrictive `tools:` filter no way to refuse a connected MCP tool.
// The ladder exemption stays; the authorial declaration now applies.
func TestLoadTool_MCPToolRespectsFilter(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	const mcpName = "mcp__chrome-devtools__take_screenshot"

	ctx := newFilteredTestCtx(t, PermissionMutating, []string{ToolView, ToolLoadTool})
	GetLoadedToolsStore().SetAvailableMCPTools(Scope(ctx.ChatID, ctx.Thread), []MCPToolInfo{
		{Name: mcpName, Description: "Capture a screenshot"},
	})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)

	assert.True(t, resp.IsError,
		"a connected MCP tool outside the declared filter must be refused, got: %s", resp.Content)
}

// TestLoadTool_MCPToolAllowedByTagLoads — an author who wants MCP tools can say
// so, and `tag:mcp` expands to the connected names.
func TestLoadTool_MCPToolAllowedByTagLoads(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	const mcpName = "mcp__chrome-devtools__take_screenshot"

	ctx := newFilteredTestCtx(t, PermissionMutating, []string{ToolView, ToolLoadTool, mcpName})
	GetLoadedToolsStore().SetAvailableMCPTools(Scope(ctx.ChatID, ctx.Thread), []MCPToolInfo{
		{Name: mcpName, Description: "Capture a screenshot"},
	})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)

	assert.False(t, resp.IsError,
		"an MCP tool named in the filter must load: %s", resp.Content)
}

// TestLoadTool_PlanModeCannotLoadWrite is the live consumer of the filter, and
// the case that matters most once the readonly tier is removed.
//
// Plan mode declares `tools: ['tag:plan', 'tag:shell']`. Its `readonly`
// permission is going away — it never prevented writes anyway, because the
// shell is granted at every tier — so the declared set becomes the only thing
// expressing "a planning agent is not handed write". This pins that it holds
// even at mutating permission.
func TestLoadTool_PlanModeCannotLoadWrite(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}

	planTools := ExpandToolFilter([]string{"tag:plan", "tag:shell"}, nil)
	ctx := newFilteredTestCtx(t, PermissionMutating, append(planTools, ToolLoadTool))

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.True(t, resp.IsError,
		"a plan-mode agent must not be able to load write: %s", resp.Content)

	// ...while its actual working set still resolves, so the guard is not just
	// refusing everything. Search is the shell in plan mode.
	shellResp, err := tool.Execute(ctx, LoadToolParams{Name: ShellToolName})
	require.NoError(t, err)
	assert.False(t, shellResp.IsError,
		"plan mode must keep the shell — it is the only search path: %s", shellResp.Content)
}

// TestSearchTools_DoesNotAdvertiseFilteredOutTools — discovery must agree with
// enforcement. Advertising a tool that load_tool will then refuse teaches the
// model to keep retrying something that cannot work.
func TestLoadTool_SearchOmitsFilteredOutTools(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newFilteredTestCtx(t, PermissionMutating, []string{ToolView, ToolLoadTool})

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "write"})
	require.NoError(t, err)

	assert.NotContains(t, resp.Content, "**"+ToolWrite+"**",
		"search must not advertise a tool the filter excludes: %s", resp.Content)
}
