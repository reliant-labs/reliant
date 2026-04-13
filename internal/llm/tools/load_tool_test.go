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
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "definitely_not_a_real_tool"})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "expected error response for unknown tool")
	assert.Contains(t, resp.Content, "not found")
}

func TestLoadTool_SearchByQuery_ReturnsMatches(t *testing.T) {
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "workflow"})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	// Several tools contain "workflow" in their name (edit_workflow, get_workflow, etc.)
	assert.Contains(t, resp.Content, "workflow")
	assert.Contains(t, resp.Content, "Found")
}

func TestLoadTool_SearchByQuery_NoMatches(t *testing.T) {
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
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "expected error when neither name nor query provided")
	assert.Contains(t, resp.Content, "name")
	assert.Contains(t, resp.Content, "query")
}

// ----- Permission gating -----

func TestLoadTool_PermissionGating_ReadOnlyCannotLoadMutatingTool(t *testing.T) {
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionReadOnly)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "readonly agent must be denied write tool")
	assert.Contains(t, resp.Content, "permission")
	assert.Contains(t, resp.Content, PermissionMutating)
}

func TestLoadTool_PermissionGating_ReadOnlyCanLoadReadOnlyTool(t *testing.T) {
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionReadOnly)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolFetch})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "readonly agent should be allowed to load fetch: %s", resp.Content)
	assert.Contains(t, resp.Content, ToolFetch)
}

func TestLoadTool_PermissionGating_MutatingCanLoadMutatingTool(t *testing.T) {
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionMutating)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "mutating agent should be allowed to load write: %s", resp.Content)
	assert.Contains(t, resp.Content, ToolWrite)
}

func TestLoadTool_PermissionGating_OrchestratorCanLoadAnything(t *testing.T) {
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

// ----- FormatDeferredToolsAnnouncement -----

func TestFormatDeferredToolsAnnouncement_ExcludesLoadedTools(t *testing.T) {
	chatID := "announce-test-loaded-" + t.Name()
	store := GetLoadedToolsStore()
	store.Clear(chatID)
	t.Cleanup(func() { store.Clear(chatID) })

	// Initial tools are whatever readonly starts with; load sourcegraph on top.
	initial := InitialToolsForPermission(PermissionOrchestrator)
	store.Add(chatID, ToolSourcegraph)

	announcement := FormatDeferredToolsAnnouncement(chatID, PermissionOrchestrator, initial, nil)
	require.NotEmpty(t, announcement, "expected a non-empty announcement when deferred tools exist")

	// The already-loaded tool must not appear in the deferred list section.
	// (It may still appear in surrounding prose; assert against the JSON-ish list.)
	assert.NotContains(t, announcement, `"`+ToolSourcegraph+`"`,
		"already-loaded tool must be excluded from the deferred announcement")
}

func TestFormatDeferredToolsAnnouncement_ExcludesInitialTools(t *testing.T) {
	chatID := "announce-test-initial-" + t.Name()
	store := GetLoadedToolsStore()
	store.Clear(chatID)
	t.Cleanup(func() { store.Clear(chatID) })

	initial := InitialToolsForPermission(PermissionOrchestrator)
	announcement := FormatDeferredToolsAnnouncement(chatID, PermissionOrchestrator, initial, nil)

	// Tools that are part of the initial set must NOT be announced as deferred.
	for _, name := range initial {
		assert.NotContains(t, announcement, `"`+name+`"`,
			"initial tool %q should not appear in the deferred announcement", name)
	}
}

func TestFormatDeferredToolsAnnouncement_EmptyWhenAllLoaded(t *testing.T) {
	chatID := "announce-test-empty-" + t.Name()
	store := GetLoadedToolsStore()
	store.Clear(chatID)
	t.Cleanup(func() { store.Clear(chatID) })

	registry := GetToolRegistry()
	allNames := make([]string, 0, len(registry))
	for _, def := range registry {
		allNames = append(allNames, def.Name)
	}

	// Even with MCP tools, if they're all in the active set, announcement should be empty
	mcpTools := []string{"mcp__test__tool"}
	allNames = append(allNames, mcpTools...)
	announcement := FormatDeferredToolsAnnouncement(chatID, PermissionOrchestrator, allNames, mcpTools)
	assert.Empty(t, announcement,
		"announcement should be empty when every registry tool and MCP tool is already accounted for")
}

func TestFormatDeferredToolsAnnouncement_IncludesMCPTools(t *testing.T) {
	chatID := "announce-test-mcp-" + t.Name()
	store := GetLoadedToolsStore()
	store.Clear(chatID)
	t.Cleanup(func() { store.Clear(chatID) })

	initial := InitialToolsForPermission(PermissionOrchestrator)
	mcpTools := []string{"mcp__server__tool1", "mcp__server__tool2"}

	announcement := FormatDeferredToolsAnnouncement(chatID, PermissionOrchestrator, initial, mcpTools)
	require.NotEmpty(t, announcement)

	// MCP tools should appear in the deferred list
	assert.Contains(t, announcement, `"mcp__server__tool1"`)
	assert.Contains(t, announcement, `"mcp__server__tool2"`)
}

func TestFormatDeferredToolsAnnouncement_ExcludesMCPToolsAlreadyActive(t *testing.T) {
	chatID := "announce-test-mcp-active-" + t.Name()
	store := GetLoadedToolsStore()
	store.Clear(chatID)
	t.Cleanup(func() { store.Clear(chatID) })

	// Put one MCP tool in the active set
	initial := append(InitialToolsForPermission(PermissionOrchestrator), "mcp__server__tool1")
	mcpTools := []string{"mcp__server__tool1", "mcp__server__tool2"}

	announcement := FormatDeferredToolsAnnouncement(chatID, PermissionOrchestrator, initial, mcpTools)
	require.NotEmpty(t, announcement)

	// Active MCP tool should NOT appear, other should
	assert.NotContains(t, announcement, `"mcp__server__tool1"`)
	assert.Contains(t, announcement, `"mcp__server__tool2"`)
}
