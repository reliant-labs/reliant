// Package wfv2 provides helper functions and accessors for proto-based workflow types.
// Provides constants and utility functions backed by proto messages
// from internal/gen/reliant/v1.
package model

// Node type constants matching the type field discriminator in YAML.
const (
	NodeTypeCallLLM        = "call_llm"
	NodeTypeExecuteTools   = "execute_tools"
	NodeTypeCompact        = "compact"
	NodeTypeApproval       = "approval"
	NodeTypeSaveMessage    = "save_message"
	NodeTypeCreateWorktree = "create_worktree"
	NodeTypeRun            = "run"
	NodeTypeWorkflow       = "workflow"
	NodeTypeLoop           = "loop"
	NodeTypeJoin           = "join"
	NodeTypeRouter         = "router"
)

// Thread mode constants.
const (
	ThreadModeInherit = "inherit"
	ThreadModeNew     = "new"
	ThreadModeFork    = "fork"
)

// Message role constants.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// Thinking level constants.
const (
	ThinkingLevelLow    = "low"
	ThinkingLevelMedium = "medium"
	ThinkingLevelHigh   = "high"
	ThinkingLevelXHigh  = "xhigh"
)

// IsActivityNode returns true if the node type is an activity (not structural).
// Discovered dynamically from NodeMeta proto annotations.
func IsActivityNode(nodeType string) bool {
	meta, ok := DiscoverNodeMetas()[nodeType]
	return ok && !meta.IsStructural
}

// IsStructuralNode returns true if the node type is structural (run, workflow, loop, join).
// Discovered dynamically from NodeMeta proto annotations.
func IsStructuralNode(nodeType string) bool {
	meta, ok := DiscoverNodeMetas()[nodeType]
	return ok && meta.IsStructural
}

// IsKnownNodeType returns true if the node type is recognized (has a NodeMeta proto annotation).
// This is the authoritative check for whether a node type string is valid.
func IsKnownNodeType(nodeType string) bool {
	_, ok := DiscoverNodeMetas()[nodeType]
	return ok
}

// KnownNodeTypes returns all known node type strings.
func KnownNodeTypes() []string {
	metas := DiscoverNodeMetas()
	types := make([]string, 0, len(metas))
	for t := range metas {
		types = append(types, t)
	}
	return types
}

// IsValidThinkingLevel returns true if the thinking level string is valid.
func IsValidThinkingLevel(level string) bool {
	switch level {
	case ThinkingLevelLow, ThinkingLevelMedium, ThinkingLevelHigh, ThinkingLevelXHigh, "":
		return true
	}
	return false
}
