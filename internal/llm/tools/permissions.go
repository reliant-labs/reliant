// Copyright (c) 2025 Reliant Labs
package tools

// Permission levels control which tools an agent is OFFERED, and which it can
// load via load_tool.
//
// THIS IS NOT A SECURITY BOUNDARY, and must not be described as one. Every tier
// holds the shell family, because search goes through the shell — so an agent
// at any level can read, write and execute whatever the daemon user can. What
// the ladder decides is which convenience tools the model is handed, which is
// steering: an agent not given `write` is far less likely to write, and that is
// genuinely useful for shaping behavior. It is not a guarantee, and code that
// needs a guarantee must get it below the tool layer.
//
// A third tier, "readonly", was removed. It sat below mutating and differed
// only in withholding write/edit/find_replace/move_code — each a one-line shell
// equivalent (`cat > f`, `sed -i`, `mv`) — while still handing over the shell.
// The name promised something the mechanism never delivered, which is worse
// than not offering it: callers reasonably read "readonly" as "cannot write".
// What that tier was really expressing — "this agent should not be handed write
// tools" — is now said directly by a workflow's `tools:` filter, which is
// enforced (see LoadedToolsStore.IsToolAllowed). A real read-only mode needs OS
// containment and will be reintroduced on that footing.
const (
	// PermissionMutating is the default: every tool except those reserved for
	// orchestrators.
	PermissionMutating = "mutating"

	// PermissionOrchestrator additionally allows spawning sub-agents.
	PermissionOrchestrator = "orchestrator"
)

// permissionOrder defines the hierarchy for comparison.
var permissionOrder = map[string]int{
	PermissionMutating:     0,
	PermissionOrchestrator: 1,
}

// PermissionAtLeast returns true if `have` is at least as permissive as `need`.
//
// An unrecognized level is NOT at least anything — including the retired
// "readonly", which a stored workflow or an in-flight run may still carry. That
// is deliberate: such a run is denied rather than silently promoted to
// mutating. See NormalizePermission for the resolution callers should apply
// when reading a level that may predate this change.
func PermissionAtLeast(have, need string) bool {
	h, ok1 := permissionOrder[have]
	n, ok2 := permissionOrder[need]
	if !ok1 || !ok2 {
		return false
	}
	return h >= n
}

// legacyReadOnlyPermission is the retired tier. Workflow YAML, presets and
// in-flight Temporal histories may still carry it, so it is recognized at the
// boundary and mapped forward rather than treated as a typo.
const legacyReadOnlyPermission = "readonly"

// NormalizePermission maps a declared permission onto a live tier.
//
// The retired "readonly" becomes "mutating", which is an honest description of
// what it always was: the shell rode along at that tier, so the agent could
// already do anything mutating could. Workflows that relied on the name to
// withhold write tools should express that in `tools:`, which enforces it —
// the builtin agent's plan mode does exactly this, and its filter is what
// actually keeps write out of a planning agent's hands.
//
// An empty or unrecognized value resolves to the default tier.
func NormalizePermission(permission string) string {
	switch permission {
	case PermissionOrchestrator:
		return PermissionOrchestrator
	case PermissionMutating:
		return PermissionMutating
	case legacyReadOnlyPermission:
		return PermissionMutating
	default:
		return PermissionMutating
	}
}

// InitialToolsForPermission returns the tool names that are always loaded
// (with full schemas) for a given permission level.
func InitialToolsForPermission(permission string) []string {
	// The shell family rides along at EVERY level because searching the codebase
	// goes through the shell — a level without it cannot search at all, which is
	// the regression that followed the removal of grep/glob. `tag:shell` is kept
	// whole (the shell's own description tells the model to reach for
	// shell_output/shell_kill/shell_list, so handing over the shell without them
	// documents tools that do not exist).
	base := []string{
		ToolSkill,
		ToolLoadTool,
		ToolView,
		ShellToolName,
		ToolShellList,
		ToolShellOutput,
		ToolShellWait,
		ToolShellKill,
		ToolFetch,
		ToolWebSearch,
		ToolWrite,
		ToolEdit,
		ToolFindReplace,
		ToolMoveCode,
	}

	// Orchestrator adds spawn, which is granted separately — so both live tiers
	// start from the same set. The switch is kept rather than collapsed because
	// an unrecognized level must still resolve to something sane.
	switch NormalizePermission(permission) {
	case PermissionMutating, PermissionOrchestrator:
		return base
	default:
		return base
	}
}

// MinimumPermissionForTool returns the minimum permission level required to load a tool.
func MinimumPermissionForTool(toolName string) string {
	// Explicit orchestrator-only tools.
	//
	// spawn_status is deliberately NOT here. An agent that already holds a
	// handle to a sub-agent it spawned needs no extra privilege to look at
	// that sub-agent or talk to it, and gating it above the tier a sub-agent
	// actually runs at only produced a warning on a tool the model was
	// correctly reaching for.
	if toolName == "spawn" || toolName == ToolAgent {
		return PermissionOrchestrator
	}

	// Everything else — including MCP and unknown tools — sits at the base tier.
	// Tag-based classification existed only to separate mutating from readonly;
	// with readonly gone there is nothing left for it to decide, and a tag table
	// that always returns the same answer reads as a gate while gating nothing.
	return PermissionMutating
}
