// Package wfv2 provides helper functions and accessors for proto-based workflow types.
// Provides constants and utility functions backed by proto messages
// from gen/reliant/v1.
package model

// Node type constants matching the type field discriminator in YAML.
const (
	NodeTypeAskQuestion    = "ask_question"
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

// Node outcome constants — the VERDICT a node stamps on the run when it
// executes (Node.outcome in the YAML). This is not the lifecycle: a run that
// routes to a failure-outcome terminal node ran to completion AND did not
// succeed, and both facts have to survive to the supervision surfaces.
//
// A node with no declaration leaves the outcome alone. Absence therefore means
// "the workflow never said", never "failure" — most workflows declare nothing.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// ValidOutcomes lists every value Node.outcome accepts, for validation and
// error messages.
var ValidOutcomes = []string{OutcomeSuccess, OutcomeFailure}

// IsValidOutcome reports whether s is a declarable node outcome.
func IsValidOutcome(s string) bool {
	return s == OutcomeSuccess || s == OutcomeFailure
}

// Thread mode constants.
const (
	ThreadModeInherit = "inherit"
	ThreadModeNew     = "new"
	ThreadModeFork    = "fork"
)

// Thread origin constants. These mirror db.ThreadOrigin*, duplicated here so
// the workflow model does not depend on the db package. Origin answers HOW a
// thread was created; it is deliberately separate from the node ID that
// created it, which answers WHICH node.
const (
	ThreadOriginMain  = "main"
	ThreadOriginSpawn = "spawn"
	ThreadOriginFork  = "fork"
	ThreadOriginNode  = "node"
)

// ThreadOriginForMode maps a thread mode to the origin of the thread it
// creates. A fork is self-describing through its fork metadata; every other
// mode handled by the inline executor is a graph node creating a thread.
func ThreadOriginForMode(mode string) string {
	if mode == ThreadModeFork {
		return ThreadOriginFork
	}
	return ThreadOriginNode
}

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
