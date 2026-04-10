// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ----- PermissionAtLeast -----

func TestPermissionAtLeast_ReadOnlyNotSufficientForMutating(t *testing.T) {
	assert.False(t, PermissionAtLeast(PermissionReadOnly, PermissionMutating),
		"readonly must not satisfy mutating requirement")
	assert.False(t, PermissionAtLeast(PermissionReadOnly, PermissionOrchestrator),
		"readonly must not satisfy orchestrator requirement")
}

func TestPermissionAtLeast_MutatingNotSufficientForOrchestrator(t *testing.T) {
	assert.False(t, PermissionAtLeast(PermissionMutating, PermissionOrchestrator),
		"mutating must not satisfy orchestrator requirement")
	// But mutating >= readonly
	assert.True(t, PermissionAtLeast(PermissionMutating, PermissionReadOnly),
		"mutating must satisfy readonly requirement")
}

func TestPermissionAtLeast_OrchestratorIsSufficientForAll(t *testing.T) {
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionReadOnly))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionMutating))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionOrchestrator))
}

func TestPermissionAtLeast_SamePermission(t *testing.T) {
	assert.True(t, PermissionAtLeast(PermissionReadOnly, PermissionReadOnly))
	assert.True(t, PermissionAtLeast(PermissionMutating, PermissionMutating))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionOrchestrator))
}

func TestPermissionAtLeast_InvalidPermission(t *testing.T) {
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
	got := InitialToolsForPermission(PermissionReadOnly)

	// Read-only agents must get the core discovery/read tools.
	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolGrep, ToolGlob, ToolFetch, ToolWebSearch,
	), "readonly initial set missing expected read-only tools: %v", got)

	// But must NOT get mutating tools.
	assert.True(t, containsNone(got,
		ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
		ToolBashList, ToolBashOutput, ToolBashKill,
	), "readonly initial set unexpectedly contains mutating tools: %v", got)
}

func TestInitialToolsForPermission_Mutating(t *testing.T) {
	got := InitialToolsForPermission(PermissionMutating)

	// Mutating agents get everything readonly gets...
	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolGrep, ToolGlob, ToolFetch, ToolWebSearch,
	), "mutating initial set missing read-only tools: %v", got)

	// ...plus all the mutating tools.
	assert.True(t, containsAll(got,
		ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
		ToolBashList, ToolBashOutput, ToolBashKill,
	), "mutating initial set missing mutating tools: %v", got)
}

func TestInitialToolsForPermission_Orchestrator(t *testing.T) {
	got := InitialToolsForPermission(PermissionOrchestrator)

	// Orchestrator at minimum contains everything the mutating level has.
	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolGrep, ToolGlob, ToolFetch, ToolWebSearch,
		ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
		ToolBashList, ToolBashOutput, ToolBashKill,
	), "orchestrator initial set missing expected tools: %v", got)

	// NOTE: The current implementation comment says "spawn is added separately",
	// so we do not assert spawn is in the initial set here — this test
	// documents that expectation. If that behavior changes, update this test.
}

// ----- MinimumPermissionForTool -----

func TestMinimumPermissionForTool_ReadOnlyTools(t *testing.T) {
	readOnlyTools := []string{ToolView, ToolGrep, ToolGlob, ToolFetch, ToolWebSearch}
	for _, name := range readOnlyTools {
		assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool(name),
			"tool %q should require readonly permission", name)
	}
}

func TestMinimumPermissionForTool_MutatingTools(t *testing.T) {
	mutatingTools := []string{ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode}
	for _, name := range mutatingTools {
		assert.Equal(t, PermissionMutating, MinimumPermissionForTool(name),
			"tool %q should require mutating permission", name)
	}
}

func TestMinimumPermissionForTool_OrchestratorTools(t *testing.T) {
	// The implementation explicitly lists "spawn" and ToolAgent as orchestrator-only.
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool("spawn"))
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolAgent))
}

func TestMinimumPermissionForTool_UnknownTool(t *testing.T) {
	// Unknown tools default to readonly (safe default per implementation comment).
	assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool("nonexistent_tool_xyz"),
		"unknown tools must default to the safest permission level")
}

func TestMinimumPermissionForTool_MCPTools(t *testing.T) {
	// MCP tools (prefix mcp__) aren't in the registry and should get the safe default.
	assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool("mcp__proxyman__get_flow_detail"))
	assert.Equal(t, PermissionReadOnly, MinimumPermissionForTool("mcp__serena__find_symbol"))
}
