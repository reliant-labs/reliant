// Copyright (c) 2025 Reliant Labs
package tools

// Permission levels control which tools an agent can load via load_tool.
const (
	// PermissionReadOnly allows read-only tools: view, fetch, websearch, skill,
	// load_tool — plus the shell, which is the ONLY search path now that the
	// scoped grep/glob tools are gone. See minimumPermissionFromTags for why
	// that makes "readonly" a weaker guarantee than its name implies.
	PermissionReadOnly = "readonly"

	// PermissionMutating allows read-only + mutating tools: write, edit, bash, find_replace, move_code, etc.
	PermissionMutating = "mutating"

	// PermissionOrchestrator allows all tools including spawn
	PermissionOrchestrator = "orchestrator"
)

// permissionOrder defines the hierarchy for comparison.
var permissionOrder = map[string]int{
	PermissionReadOnly:     0,
	PermissionMutating:     1,
	PermissionOrchestrator: 2,
}

// PermissionAtLeast returns true if `have` is at least as permissive as `need`.
func PermissionAtLeast(have, need string) bool {
	h, ok1 := permissionOrder[have]
	n, ok2 := permissionOrder[need]
	if !ok1 || !ok2 {
		return false
	}
	return h >= n
}

// InitialToolsForPermission returns the tool names that are always loaded
// (with full schemas) for a given permission level.
func InitialToolsForPermission(permission string) []string {
	// All levels get these. The shell family rides along at EVERY level because
	// searching the codebase now goes through the shell — a level without it
	// cannot search at all, which is the regression that followed the first
	// removal of grep/glob. `tag:shell` is kept whole (the shell's own
	// description tells the model to reach for bash_output/bash_kill/bash_list,
	// so handing over the shell without them documents tools that do not exist).
	base := []string{
		ToolSkill,
		ToolLoadTool,
		ToolView,
		ShellToolName,
		ToolBashList,
		ToolBashOutput,
		ToolBashWait,
		ToolBashKill,
	}

	switch permission {
	case PermissionReadOnly:
		return append(base, ToolFetch, ToolWebSearch)
	case PermissionMutating:
		return append(base,
			ToolFetch, ToolWebSearch,
			ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode,
		)
	case PermissionOrchestrator:
		// Orchestrator gets everything mutating gets, plus spawn is added separately
		return append(base,
			ToolFetch, ToolWebSearch,
			ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode,
		)
	default:
		return base
	}
}

// MinimumPermissionForTool returns the minimum permission level required to load a tool.
// Uses tool tags from the registry to determine the level.
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

	// Check the registry for tag-based classification
	registry := GetToolRegistry()
	for _, def := range registry {
		if def.Name == toolName {
			return minimumPermissionFromTags(def.Tags)
		}
	}

	// MCP tools and unknown tools default to readonly (safe default)
	return PermissionReadOnly
}

// minimumPermissionFromTags determines permission level from tool tags.
//
// The shell family is readonly-tier. That is a deliberate weakening, not an
// oversight: with the scoped grep/glob tools removed, the shell is the only way
// to search a codebase, so gating it at mutating would leave readonly and
// plan-mode agents unable to search — the exact regression the earlier removal
// produced. The tradeoff is that "readonly" no longer means the agent cannot
// write; it means the agent is not HANDED write tools, while retaining a shell
// that can still `>` a file. Callers needing a hard read-only boundary must
// enforce it below the tool layer (sandbox/filesystem), not via this gate.
func minimumPermissionFromTags(tags []ToolTag) string {
	for _, tag := range tags {
		switch tag {
		case TagShell, TagExecution:
			return PermissionReadOnly
		case TagFile:
			// File tools that are also read-only stay readonly
			for _, t := range tags {
				if t == TagReadOnly {
					return PermissionReadOnly
				}
			}
			return PermissionMutating
		}
	}
	return PermissionReadOnly
}
