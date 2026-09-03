// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ----- PermissionAtLeast -----

func TestPermissionAtLeast_ReadOnlyNotSufficientForMutating(t *testing.T) {
	t.Parallel()
	assert.False(t, PermissionAtLeast(PermissionReadOnly, PermissionMutating),
		"readonly must not satisfy mutating requirement")
	assert.False(t, PermissionAtLeast(PermissionReadOnly, PermissionOrchestrator),
		"readonly must not satisfy orchestrator requirement")
}

func TestPermissionAtLeast_MutatingNotSufficientForOrchestrator(t *testing.T) {
	t.Parallel()
	assert.False(t, PermissionAtLeast(PermissionMutating, PermissionOrchestrator),
		"mutating must not satisfy orchestrator requirement")
	// But mutating >= readonly
	assert.True(t, PermissionAtLeast(PermissionMutating, PermissionReadOnly),
		"mutating must satisfy readonly requirement")
}

func TestPermissionAtLeast_OrchestratorIsSufficientForAll(t *testing.T) {
	t.Parallel()
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionReadOnly))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionMutating))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionOrchestrator))
}

func TestPermissionAtLeast_SamePermission(t *testing.T) {
	t.Parallel()
	assert.True(t, PermissionAtLeast(PermissionReadOnly, PermissionReadOnly))
	assert.True(t, PermissionAtLeast(PermissionMutating, PermissionMutating))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionOrchestrator))
}

func TestPermissionAtLeast_InvalidPermission(t *testing.T) {
	t.Parallel()
	// Unknown permissions must not satisfy any real permission requirement.
	assert.False(t, PermissionAtLeast("bogus", PermissionReadOnly),
		"unknown 'have' permission must be treated as insufficient")
	assert.False(t, PermissionAtLeast(PermissionOrchestrator, "bogus"),
		"unknown 'need' permission must return false (safe default)")
	assert.False(t, PermissionAtLeast("", ""),
		"empty permissions must be treated as insufficient")
}

// ----- InitialToolsForPermission -----

// containsAll returns true if every needle appears in haystack.
func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]bool, len(haystack))
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

// containsNone returns true if none of the needles appear in haystack.
func containsNone(haystack []string, needles ...string) bool {
	set := make(map[string]bool, len(haystack))
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if set[n] {
			return false
		}
	}
	return true
}

func TestInitialToolsForPermission_ReadOnly(t *testing.T) {
	t.Parallel()
	got := InitialToolsForPermission(PermissionReadOnly)

	// Read-only agents must get the core discovery/read tools.
	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolFetch, ToolWebSearch,
	), "readonly initial set missing expected read-only tools: %v", got)

	// The shell family rides along even at readonly, because it is the only way
	// to search a codebase now that the scoped grep/glob tools are gone. A
	// readonly agent without it cannot search at all.
	assert.True(t, containsAll(got,
		ShellToolName, ToolBashList, ToolBashOutput, ToolBashKill,
	), "readonly initial set missing the shell search path: %v", got)

	// But must NOT get the file-mutating tools.
	assert.True(t, containsNone(got,
		ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode,
	), "readonly initial set unexpectedly contains mutating tools: %v", got)
}

func TestInitialToolsForPermission_Mutating(t *testing.T) {
	t.Parallel()
	got := InitialToolsForPermission(PermissionMutating)

	// Mutating agents get everything readonly gets...
	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolFetch, ToolWebSearch,
	), "mutating initial set missing read-only tools: %v", got)

	// ...plus all the mutating tools.
	assert.True(t, containsAll(got,
		ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
		ToolBashList, ToolBashOutput, ToolBashKill,
	), "mutating initial set missing mutating tools: %v", got)
}

func TestInitialToolsForPermission_Orchestrator(t *testing.T) {
	t.Parallel()
	got := InitialToolsForPermission(PermissionOrchestrator)

	// Orchestrator at minimum contains everything the mutating level has.
	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolFetch, ToolWebSearch,
		ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
		ToolBashList, ToolBashOutput, ToolBashKill,
	), "orchestrator initial set missing expected tools: %v", got)

	// NOTE: The current implementation comment says "spawn is added separately",
	// so we do not assert spawn is in the initial set here — this test
	// documents that expectation. If that behavior changes, update this test.
}

// ----- MinimumPermissionForTool -----

func TestMinimumPermissionForTool_ReadOnlyTools(t *testing.T) {
	t.Parallel()
	readOnlyTools := []string{ToolView, ToolFetch, ToolWebSearch}
	for _, name := range readOnlyTools {
		assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool(name),
			"tool %q should require readonly permission", name)
	}
}

func TestMinimumPermissionForTool_MutatingTools(t *testing.T) {
	t.Parallel()
	mutatingTools := []string{ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode}
	for _, name := range mutatingTools {
		assert.Equal(t, PermissionMutating, MinimumPermissionForTool(name),
			"tool %q should require mutating permission", name)
	}
}

// The shell gates at READONLY, not mutating. This is the deliberate tradeoff
// taken when the scoped grep/glob tools were removed: the shell became the only
// search path, so gating it at mutating would leave readonly and plan-mode
// agents unable to search — the regression that followed the first removal.
// "readonly" therefore means "not handed write tools", not "cannot write": the
// shell can still redirect into a file. A hard read-only boundary has to be
// enforced below the tool layer.
func TestMinimumPermissionForTool_ShellIsReadOnlyTier(t *testing.T) {
	t.Parallel()
	for _, name := range []string{ShellToolName, ToolBashList, ToolBashOutput, ToolBashKill} {
		assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool(name),
			"tool %q must be loadable by a readonly agent so search still works", name)
	}
}

func TestMinimumPermissionForTool_OrchestratorTools(t *testing.T) {
	t.Parallel()
	// The implementation explicitly lists "spawn" and ToolAgent as orchestrator-only.
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool("spawn"))
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolAgent))
}

func TestMinimumPermissionForTool_UnknownTool(t *testing.T) {
	t.Parallel()
	// Unknown tools default to readonly (safe default per implementation comment).
	assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool("nonexistent_tool_xyz"),
		"unknown tools must default to the safest permission level")
}

func TestMinimumPermissionForTool_MCPTools(t *testing.T) {
	t.Parallel()
	// MCP tools (prefix mcp__) aren't in the registry and should get the safe default.
	assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool("mcp__proxyman__get_flow_detail"))
	assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool("mcp__serena__find_symbol"))
}
