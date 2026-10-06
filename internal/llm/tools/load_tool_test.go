// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLoadToolTestCtx is a tool context for a turn whose node declared
// loadable_tools: ["*"] at the given tier and was offered nothing yet — the
// shape the default agents run load_tool in.
func newLoadToolTestCtx(t *testing.T, permission string) *rctx.ToolContext {
	t.Helper()
	return toolCtxWithCaps(t, &Capabilities{LoadableAll: true, Permission: permission})
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
// are absent from tag:coding:default and thus never in a general-preset agent's
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
		assert.Equal(t, []string{name}, grantsOf(t, loadResp), "%q must be granted", name)
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
	ctx := toolCtxWithCaps(t, &Capabilities{LoadableAll: true, Permission: PermissionOrchestrator,
		MCPTools: []string{"mcp__chrome-devtools__take_screenshot"}})

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "screenshot"})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "mcp__chrome-devtools__take_screenshot",
		"search should surface a connected MCP tool matched by keyword")
}

func TestLoadTool_LoadMCPTool_ConnectedSucceeds(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	const mcpName = "mcp__chrome-devtools__take_screenshot"
	ctx := toolCtxWithCaps(t, &Capabilities{LoadableAll: true, Permission: PermissionOrchestrator,
		MCPTools: []string{mcpName}})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: mcpName})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "connected MCP tool should load: %s", resp.Content)
	assert.Contains(t, resp.Content, mcpName)
	assert.Equal(t, []string{mcpName}, grantsOf(t, resp), "the connected MCP tool must be granted")
}

func TestLoadTool_LoadMCPTool_UnconnectedErrors(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	// A different MCP tool is connected; request one that is not.
	ctx := toolCtxWithCaps(t, &Capabilities{LoadableAll: true, Permission: PermissionOrchestrator,
		MCPTools: []string{"mcp__chrome-devtools__navigate_page"}})

	const missing = "mcp__chrome-devtools__take_screenshot"
	resp, err := tool.Execute(ctx, LoadToolParams{Name: missing})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "unconnected MCP tool must error instead of being silently added")
	assert.Contains(t, resp.Content, "not available")
	assert.Empty(t, grantsOf(t, resp), "an unavailable MCP tool must NOT be granted")
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

// The readonly tier's gating tests are gone with the tier. What they were
// really asserting — that an agent scoped to reading cannot acquire `write` —
// is now enforced by the declared tool set and tested in load_tool_filter_test.go
// (TestLoadTool_CannotEscapeDeclaredFilter, TestLoadTool_PlanModeCannotLoadWrite).
// That is a stronger test than the old one, which passed while the same agent
// held a shell that could `> file`.

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

// ----- Grants -----

// A grant is reported, not stored: execute_tools reads it from the metadata
// into granted_tools, and the workflow hands it to the thread's next call_llm.
func TestLoadTool_GrantIsReportedInMetadata(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	require.False(t, resp.IsError, "load must succeed: %s", resp.Content)
	assert.Equal(t, []string{ToolWrite}, grantsOf(t, resp))
}

// "Already loaded" means offered on this turn — the set is the only record.
func TestLoadTool_LoadOfferedTool_SaysAlreadyLoaded(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := toolCtxWithCaps(t, &Capabilities{LoadableAll: true, Permission: PermissionOrchestrator,
		Offered: []string{ToolLoadTool, ToolWrite}})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "loading an already-loaded tool must not error")
	assert.Contains(t, strings.ToLower(resp.Content), "already loaded")
	assert.Empty(t, grantsOf(t, resp), "nothing new is granted")
}

func TestLoadTool_DeniedLoadGrantsNothing(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	// start_run is orchestrator-only, so it is what a tier denial is tested with.
	ctx := newLoadToolTestCtx(t, PermissionMutating)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolStartRun})
	require.NoError(t, err)
	require.True(t, resp.IsError, "a mutating agent must be denied start_run")
	assert.Contains(t, resp.Content, "requires 'orchestrator' permission")
	assert.Empty(t, grantsOf(t, resp), "a denied load must grant nothing")
}

// With no set on the context — a batch whose call_llm recorded none, from a
// run that predates it — load_tool falls back to the chat row's boundary.
func TestLoadTool_NoRecordedSet_StaysWithinTheChatRow(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	ctx := rctx.NewToolContext(nomachine.With(context.Background()), "legacy-"+t.Name(), "0", nil, worktree)

	refused, err := tool.Execute(ctx, LoadToolParams{Name: ToolView})
	require.NoError(t, err)
	assert.True(t, refused.IsError, "a no-machine chat must not load a machine tool: %s", refused.Content)

	loaded, err := tool.Execute(ctx, LoadToolParams{Name: ToolFetch})
	require.NoError(t, err)
	assert.False(t, loaded.IsError, loaded.Content)
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

// ----- Tag loading -----

func workflowTagToolNames() []string {
	var names []string
	for _, def := range GetToolRegistry() {
		for _, tag := range def.Tags {
			if tag == TagWorkflow {
				names = append(names, def.Name)
			}
		}
	}
	return names
}

func TestLoadTool_TagWorkflow_LoadsAllTwenty(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)
	want := workflowTagToolNames()
	require.Len(t, want, 20)
	// Integration discovery rides with the workflow tools: an agent writing an
	// action node needs to find its ref and schema.
	assert.Contains(t, want, ToolSearchIntegrations)
	assert.Contains(t, want, ToolGetIntegrationSchema)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "tag:workflow"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.ElementsMatch(t, want, grantsOf(t, resp))

	// On the next turn they are offered, and loading the tag again is a no-op.
	nextTurn := toolCtxWithCaps(t, &Capabilities{LoadableAll: true, Permission: PermissionOrchestrator,
		Offered: sortedUnique(append([]string{ToolLoadTool}, want...))})
	again, err := tool.Execute(nextTurn, LoadToolParams{Name: "tag:workflow"})
	require.NoError(t, err)
	assert.Contains(t, again.Content, "20 already loaded")
	assert.Empty(t, grantsOf(t, again))
}

func TestLoadTool_TagWorkflow_RestrictedLoadableReportsRefused(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := toolCtxWithCaps(t, &Capabilities{Permission: PermissionOrchestrator,
		Loadable: sortedUnique([]string{ToolGetWorkflow, ToolListWorkflows})})

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "tag:workflow"})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "2 loaded")
	assert.Contains(t, resp.Content, "18 refused")
	assert.Contains(t, resp.Content, "not loadable")

	assert.ElementsMatch(t, []string{ToolGetWorkflow, ToolListWorkflows}, grantsOf(t, resp))
}

func TestLoadTool_TagUnknown_NamesKnownTags(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "tag:nope"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "Unknown tag")
	assert.Contains(t, resp.Content, string(TagWorkflow))
}

func TestLoadTool_SearchByTagName_ReturnsAllWorkflowTools(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Query: "workflow"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	for _, name := range workflowTagToolNames() {
		assert.Contains(t, resp.Content, "**"+name+"**")
	}
	// Plus anything NAMED for workflows without the tag (github__workflow_dispatch).
	matches := 0
	for _, def := range GetToolRegistry() {
		if def.hasTag(TagWorkflow) || strings.Contains(def.Name, "workflow") {
			matches++
		}
	}
	assert.Contains(t, resp.Content, fmt.Sprintf("Found %d tools", matches))
}

func TestLoadTool_TagWithNoRegistryTools_SaysSo(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionOrchestrator)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "tag:mcp"})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "no built-in tools carry it")
	assert.Contains(t, resp.Content, "mcp__")
	assert.NotContains(t, resp.Content, "0 loaded")
}

func TestLoadTool_TagRuns_PermissionLadderRefusesOrchestratorTools(t *testing.T) {
	t.Parallel()
	tool := &loadToolTool{}
	ctx := newLoadToolTestCtx(t, PermissionMutating)

	resp, err := tool.Execute(ctx, LoadToolParams{Name: "tag:runs"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	loaded := grantsOf(t, resp)
	for _, name := range []string{ToolStartRun, ToolControlRun, ToolSendToRun} {
		assert.NotContains(t, loaded, name)
		assert.Contains(t, resp.Content, name+" (Tool '"+name+"' requires 'orchestrator' permission")
	}
	assert.Contains(t, loaded, ToolListRuns)
	assert.Contains(t, loaded, ToolGetRun)
}
