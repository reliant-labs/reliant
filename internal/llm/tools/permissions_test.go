// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The ladder has two live tiers. Everything that used to distinguish a third,
// "readonly", is now said by a workflow's `tools:` filter — see
// load_tool_filter_test.go, which is where the enforceable half of this story
// is tested.

// ----- PermissionAtLeast -----

func TestPermissionAtLeast_MutatingNotSufficientForOrchestrator(t *testing.T) {
	t.Parallel()
	assert.False(t, PermissionAtLeast(PermissionMutating, PermissionOrchestrator),
		"mutating must not satisfy orchestrator requirement")
}

func TestPermissionAtLeast_OrchestratorIsSufficientForAll(t *testing.T) {
	t.Parallel()
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionMutating))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionOrchestrator))
}

func TestPermissionAtLeast_SamePermission(t *testing.T) {
	t.Parallel()
	assert.True(t, PermissionAtLeast(PermissionMutating, PermissionMutating))
	assert.True(t, PermissionAtLeast(PermissionOrchestrator, PermissionOrchestrator))
}

func TestPermissionAtLeast_InvalidPermission(t *testing.T) {
	t.Parallel()
	assert.False(t, PermissionAtLeast("bogus", PermissionMutating),
		"unknown 'have' permission must be treated as insufficient")
	assert.False(t, PermissionAtLeast(PermissionOrchestrator, "bogus"),
		"unknown 'need' permission must return false (safe default)")
	assert.False(t, PermissionAtLeast("", ""),
		"empty permissions must be treated as insufficient")
}

// The retired tier is not silently promoted by the comparison itself — a stored
// "readonly" is an unknown level here and fails closed. Callers that may read a
// pre-change value run it through NormalizePermission first.
func TestPermissionAtLeast_RetiredReadOnlyIsNotRecognized(t *testing.T) {
	t.Parallel()
	assert.False(t, PermissionAtLeast("readonly", PermissionMutating),
		"the retired tier must not satisfy a real requirement without normalization")
}

// ----- NormalizePermission -----

// A workflow, preset, or in-flight Temporal history written before the tier was
// removed still carries "readonly". It resolves to mutating, which is an honest
// description of what that tier always was: the shell rode along at it, so the
// agent could already do anything mutating could.
func TestNormalizePermission_RetiredReadOnlyBecomesMutating(t *testing.T) {
	t.Parallel()
	assert.Equal(t, PermissionMutating, NormalizePermission("readonly"))
}

func TestNormalizePermission_LiveTiersUnchanged(t *testing.T) {
	t.Parallel()
	assert.Equal(t, PermissionMutating, NormalizePermission(PermissionMutating))
	assert.Equal(t, PermissionOrchestrator, NormalizePermission(PermissionOrchestrator))
}

func TestNormalizePermission_UnknownFallsBackToBaseTier(t *testing.T) {
	t.Parallel()
	assert.Equal(t, PermissionMutating, NormalizePermission(""))
	assert.Equal(t, PermissionMutating, NormalizePermission("bogus"))
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

func TestInitialToolsForPermission_Mutating(t *testing.T) {
	t.Parallel()
	got := InitialToolsForPermission(PermissionMutating)

	assert.True(t, containsAll(got,
		ToolSkill, ToolLoadTool, ToolView, ToolFetch, ToolWebSearch,
	), "mutating initial set missing read tools: %v", got)

	assert.True(t, containsAll(got,
		ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode,
	), "mutating initial set missing mutating tools: %v", got)

	// The shell family is kept whole — the shell's own description tells the
	// model to reach for shell_output/shell_list/shell_kill, so handing over the
	// shell without them documents tools that do not exist.
	assert.True(t, containsAll(got,
		ShellToolName, ToolShellList, ToolShellOutput, ToolShellWait, ToolShellKill,
	), "mutating initial set missing the shell family: %v", got)
}

func TestInitialToolsForPermission_Orchestrator(t *testing.T) {
	t.Parallel()
	// Both live tiers start from the same set; orchestrator's extra capability
	// is spawn, which is granted separately rather than through this list.
	assert.Equal(t,
		InitialToolsForPermission(PermissionMutating),
		InitialToolsForPermission(PermissionOrchestrator),
		"orchestrator differs from mutating only by spawn, which is granted separately")
}

func TestInitialToolsForPermission_RetiredTierStillResolves(t *testing.T) {
	t.Parallel()
	// A stored "readonly" must not produce an empty or degraded tool set.
	assert.Equal(t,
		InitialToolsForPermission(PermissionMutating),
		InitialToolsForPermission("readonly"),
		"the retired tier must resolve to the base set, not to nothing")
}

// A stored workflow declaring the retired tier must still run, and must resolve
// to a tier that actually clears the gates its tools sit behind. This is the
// upgrade path: a `permission: readonly` workflow keeps working, it just stops
// implying a guarantee it never provided.
func TestRetiredReadOnlyWorkflowStillClearsItsGates(t *testing.T) {
	t.Parallel()

	resolved := NormalizePermission("readonly")

	for _, name := range []string{ToolView, ShellToolName, ToolWrite, ToolEdit} {
		assert.True(t, PermissionAtLeast(resolved, MinimumPermissionForTool(name)),
			"a normalized legacy workflow must still be able to run %q", name)
	}

	// ...but it does not silently gain the one capability the ladder still gates.
	assert.False(t, PermissionAtLeast(resolved, MinimumPermissionForTool("spawn")),
		"normalizing the retired tier must not grant spawn")
}

// ----- MinimumPermissionForTool -----

func TestMinimumPermissionForTool_OrchestratorTools(t *testing.T) {
	t.Parallel()
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool("spawn"))
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolAgent))
}

// Everything that is not spawn sits at the base tier. With readonly gone there
// is nothing for tag-based classification to decide, so the answer is uniform —
// which is the point: the ladder no longer pretends to separate reading from
// writing, because it never could once the shell was granted at every level.
func TestMinimumPermissionForTool_EverythingElseIsBaseTier(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		ToolView, ToolFetch, ToolWebSearch,
		ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode,
		ShellToolName, ToolShellList, ToolShellOutput, ToolShellKill,
		"nonexistent_tool_xyz",
		"mcp__proxyman__get_flow_detail",
	} {
		assert.Equal(t, PermissionMutating, MinimumPermissionForTool(name),
			"tool %q should sit at the base tier", name)
	}
}

// spawn_status and spawn_send stay reachable below orchestrator: an agent
// holding a handle to a sub-agent it spawned needs no extra privilege to look at
// it or talk to it, and a sub-agent itself does not run at orchestrator tier.
func TestMinimumPermissionForTool_SpawnObservabilityNotGated(t *testing.T) {
	t.Parallel()
	assert.Equal(t, PermissionMutating, MinimumPermissionForTool(ToolSpawnStatus))
	assert.Equal(t, PermissionMutating, MinimumPermissionForTool(ToolSpawnSend))
}
