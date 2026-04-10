// Copyright (c) 2025 Reliant Labs
package tools

// Permission levels control which tools an agent can load via load_tool.
const (
	// PermissionReadOnly allows read-only tools: view, grep, glob, fetch, websearch, skill, load_tool
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
	// All levels get these
	base := []string{
		ToolSkill,
		ToolLoadTool,
		ToolView,
		ToolGrep,
		ToolGlob,
	}

	switch permission {
	case PermissionReadOnly:
		return append(base, ToolFetch, ToolWebSearch)
	case PermissionMutating:
		return append(base,
			ToolFetch, ToolWebSearch,
			ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
			ToolBashList, ToolBashOutput, ToolBashKill,
		)
	case PermissionOrchestrator:
		// Orchestrator gets everything mutating gets, plus spawn is added separately
		return append(base,
			ToolFetch, ToolWebSearch,
			ToolWrite, ToolEdit, ShellToolName, ToolFindReplace, ToolMoveCode,
			ToolBashList, ToolBashOutput, ToolBashKill,
		)
	default:
		return base
	}
}

// MinimumPermissionForTool returns the minimum permission level required to load a tool.
// Uses tool tags from the registry to determine the level.
func MinimumPermissionForTool(toolName string) string {
	// Explicit orchestrator-only tools
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
func minimumPermissionFromTags(tags []ToolTag) string {
	for _, tag := range tags {
		switch tag {
		case TagShell, TagExecution:
			return PermissionMutating
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
