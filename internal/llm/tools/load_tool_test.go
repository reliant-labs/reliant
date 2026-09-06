// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLoadToolTestCtx creates a ToolContext bound to a unique chat ID for the
// running test and arranges cleanup of the global LoadedToolsStore entry.
func newLoadToolTestCtx(t *testing.T, permission string) *rctx.ToolContext {
	t.Helper()
	chatID := "loadtool-test-" + t.Name()

	store := GetLoadedToolsStore()
	store.Clear(chatID) // start clean in case a prior run left state
	store.SetPermission(chatID, permission)

	t.Cleanup(func() {
		store.Clear(chatID)
	})

	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	return rctx.NewToolContext(context.Background(), chatID, "0", nil, worktree)
}

// ----- Load / search behavior -----

func TestLoadTool_LoadByName_Success(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolSourcegraph})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "expected success, got error: %s", resp.Content)
	assert.Contains(t, resp.Content, ToolSourcegraph)
	assert.Contains(t, resp.Content, "loaded")

	// Metadata should announce the loaded tool for the runtime.
	assert.Contains(t, resp.Metadata, ToolSourcegraph,
		"metadata should contain the loaded tool name for the runtime")
}

func TestLoadTool_LoadByName_NonexistentTool(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "definitely_not_a_real_tool"})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "expected error response for unknown tool")
	assert.Contains(t, resp.Content, "not found")
}

func TestLoadTool_SearchByQuery_ReturnsMatches(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "workflow"})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	// Several tools contain "workflow" in their name (edit_workflow, get_workflow, etc.)
	assert.Contains(t, resp.Content, "workflow")
	assert.Contains(t, resp.Content, "Found")
}

// TestLoadTool_GeneralPresetAgentCanReachWorkflowTools pins the requirement
// that a plain chat gets the same workflow-building capability as the
// dedicated workflow-builder UI. Workflow tools carry only TagWorkflow — they
// are absent from tag:default and thus never in a general-preset agent's
// initial tool set — so the only path to them is load_tool. This asserts that
// path is not blocked by permission gating (general runs at "mutating", not
// "orchestrator") and that the tools are actually loadable, not just named in
// a search result.
func TestLoadTool_GeneralPresetAgentCanReachWorkflowTools(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	// "general" (internal/workflow/builtin/presets/general.yaml) runs its
	// call_llm node at mutating permission - it is not an orchestrator preset.
	ctx := newLoadToolTestCtx(t, PermissionMutating)

	searchResp, err := tool.Execute(ctx, LoadToolParams{Query: "workflow"})
	require.NoError(t, err)
	require.False(t, searchResp.IsError, "search must succeed: %s", searchResp.Content)
	for _, name := range []string{ToolCreateWorkflow, ToolEditWorkflow, ToolListWorkflows, ToolGetWorkflow} {
		assert.Contains(t, searchResp.Content, name,
			"load_tool(query=\"workflow\") must surface %q to a default/mutating agent", name)
		assert.NotContains(t, searchResp.Content, name+"** [workflow] (requires orchestrator permission)",
			"%q must not report itself as gated above what general's mutating tier can reach", name)
	}

	for _, name := range []string{ToolCreateWorkflow, ToolEditWorkflow, ToolListWorkflows, ToolGetWorkflow} {
		loadResp, err := tool.Execute(ctx, LoadToolParams{Name: name})
		require.NoError(t, err)
		assert.False(t, loadResp.IsError,
			"a mutating-permission agent must be able to load %q via load_tool: %s", name, loadResp.Content)
	}

	loaded := GetLoadedToolsStore().Get(ctx.ChatID)
	for _, name := range []string{ToolCreateWorkflow, ToolEditWorkflow, ToolListWorkflows, ToolGetWorkflow} {
		assert.Contains(t, loaded, name, "%q should be recorded as loaded for this chat", name)
	}
}

func TestLoadTool_SearchByQuery_NoMatches(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "zzz_no_such_keyword_zzz"})
	require.NoError(t, err)
	// The implementation returns a normal (non-error) text response with a
	// helpful "no tools found" message.
	assert.False(t, resp.IsError)
	assert.Contains(t, strings.ToLower(resp.Content), "no tools found")
}

func TestLoadTool_EmptyParams_ReturnsError(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "expected error when neither name nor query provided")
	assert.Contains(t, resp.Content, "name")
	assert.Contains(t, resp.Content, "query")
}

// ----- MCP tool discovery / loading -----

func TestLoadTool_SearchByQuery_SurfacesConnectedMCPTool(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	GetLoadedToolsStore().SetAvailableMCPTools(ctx.ChatID, []MCPToolInfo{
		{Name: "mcp__chrome-devtools__take_screenshot", Description: "Capture a screenshot"},
	})

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "screenshot"})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "mcp__chrome-devtools__take_screenshot",
		"search should surface a connected MCP tool matched by keyword")
}

func TestLoadTool_LoadMCPTool_ConnectedSucceeds(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	const mcpName = "mcp__chrome-devtools__take_screenshot"
	GetLoadedToolsStore().SetAvailableMCPTools(ctx.ChatID, []MCPToolInfo{
		{Name: mcpName, Description: "Capture a screenshot"},
	})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "connected MCP tool should load: %s", resp.Content)
	assert.Contains(t, resp.Content, mcpName)
	assert.Contains(t, resp.Metadata, mcpName, "metadata should announce the loaded MCP tool")
	assert.True(t, GetLoadedToolsStore().Has(ctx.ChatID, mcpName),
		"connected MCP tool must be recorded in the store")
}

func TestLoadTool_LoadMCPTool_UnconnectedErrors(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	// A different MCP tool is connected; request one that is not.
	GetLoadedToolsStore().SetAvailableMCPTools(ctx.ChatID, []MCPToolInfo{
		{Name: "mcp__chrome-devtools__navigate_page", Description: "Navigate"},
	})

	const missing = "mcp__chrome-devtools__take_screenshot"
	resp, err := tool.Execute(ctx, LoadToolParams{Name: missing})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "unconnected MCP tool must error instead of being silently added")
	assert.Contains(t, resp.Content, "not available")
	assert.False(t, GetLoadedToolsStore().Has(ctx.ChatID, missing),
		"unavailable MCP tool must NOT be recorded in the store")
}

func TestLoadTool_LoadMCPTool_NoneConnectedErrors(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	// No MCP tools recorded for this chat at all.
	resp, err := tool.Execute(ctx, LoadToolParams{Name: "mcp__server__tool"})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "no MCP tools connected -> load must error")
	assert.Contains(t, resp.Content, "not available")
}

// ----- Permission gating -----

func TestLoadTool_PermissionGating_ReadOnlyCannotLoadMutatingTool(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionReadOnly)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "readonly agent must be denied write tool")
	assert.Contains(t, resp.Content, "permission")
	assert.Contains(t, resp.Content, PermissionMutating)
}

func TestLoadTool_PermissionGating_ReadOnlyCanLoadReadOnlyTool(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionReadOnly)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolFetch})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "readonly agent should be allowed to load fetch: %s", resp.Content)
	assert.Contains(t, resp.Content, ToolFetch)
}

func TestLoadTool_PermissionGating_MutatingCanLoadMutatingTool(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionMutating)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "mutating agent should be allowed to load write: %s", resp.Content)
	assert.Contains(t, resp.Content, ToolWrite)
}

func TestLoadTool_PermissionGating_OrchestratorCanLoadAnything(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}

	// Try a representative set spanning readonly and mutating tools.
	cases := []string{ToolFetch, ToolWrite, ToolEdit, ShellToolName, ToolMoveCode}
	for _, name := range cases {
		// Use a separate chat per sub-case so "already loaded" doesn't interfere.
		t.Run(name, func(t *testing.T) {
			subCtx := newLoadToolTestCtx(t, PermissionOrchestrator)
			resp, err := tool.Execute(subCtx, LoadToolParams{Name: name})
			require.NoError(t, err)
			assert.False(t, resp.IsError,
				"orchestrator should be able to load %q: %s", name, resp.Content)
		})
	}
}

// ----- Store integration -----

func TestLoadTool_StoresInLoadedToolsStore(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	store := GetLoadedToolsStore()
	assert.False(t, store.Has(ctx.ChatID, ToolWrite), "precondition: not yet loaded")

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	require.False(t, resp.IsError, "load must succeed: %s", resp.Content)

	assert.True(t, store.Has(ctx.ChatID, ToolWrite),
		"successfully loaded tool must be recorded in the store")
}

func TestLoadTool_LoadAlreadyLoadedTool_Idempotent(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	// First load
	resp1, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	require.False(t, resp1.IsError)

	// Second load
	resp2, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.False(t, resp2.IsError, "loading an already-loaded tool must not error")
	assert.Contains(t, strings.ToLower(resp2.Content), "already loaded")

	// Still exactly one entry.
	store := GetLoadedToolsStore()
	loaded := store.Get(ctx.ChatID)
	assert.Equal(t, []string{ToolWrite}, loaded)
}

func TestLoadTool_DeniedLoadNotStored(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionReadOnly)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	require.True(t, resp.IsError, "readonly must be denied write")

	store := GetLoadedToolsStore()
	assert.False(t, store.Has(ctx.ChatID, ToolWrite),
		"permission-denied load must NOT be recorded in the store")
	assert.Nil(t, store.Get(ctx.ChatID),
		"store should have no loaded tools after a denied load")
}

// ----- SetDeferredTools / Description -----

func TestLoadTool_DescriptionIncludesDeferredTools(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}

	// Without deferred tools, description is the base text.
	assert.Equal(t, loadToolDescription, tool.Description())

	// Set deferred tools and verify they appear in the description.
	tool.SetDeferredTools([]string{"sourcegraph", "mcp__server__tool1"})
	desc := tool.Description()
	assert.Contains(t, desc, `"sourcegraph"`)
	assert.Contains(t, desc, `"mcp__server__tool1"`)
	assert.Contains(t, desc, "Additional tools available")
}

func TestLoadTool_DescriptionNoDeferredTools(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	tool.SetDeferredTools(nil)
	assert.Equal(t, loadToolDescription, tool.Description())

	tool.SetDeferredTools([]string{})
	assert.Equal(t, loadToolDescription, tool.Description())
}

func TestLoadTool_ImplementsDeferredToolsAware(t *testing.T) {
	t.Parallel()
	tool := NewLoadToolTool()
	u, ok := tool.(interface{ Unwrap() any })
	require.True(t, ok, "load_tool wrapper must implement Unwrap")
	_, ok = u.Unwrap().(DeferredToolsAware)
	assert.True(t, ok, "inner load_tool must implement DeferredToolsAware")
}
