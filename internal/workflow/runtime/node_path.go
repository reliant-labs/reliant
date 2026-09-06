// Copyright (c) 2025 Reliant Labs
package runtime

// joinNodePath composes a child's fully-qualified graph path from the path of
// the scope that contains it.
//
// This is COMPOSITION, not replacement. Every nesting boundary — a loop body, a
// sub-workflow body, a loop inside a sub-workflow inside a loop — appends one
// more segment, so a node three levels down reports
// "impl_loop.attempt.review" rather than the outermost prefix or the bare id.
// Keeping only the outermost prefix (what the loop-scoped LoopNodeID does, by
// design) makes the path unrecoverable from two levels down.
//
// The dotted form is the convention scenarios already use for `reached:` /
// `not_reached:` and the one the fast simulator emits, so a path built here is
// directly comparable to a simulator node id.
//
// An empty prefix yields the bare id: a top-level node's path is its own id.
func joinNodePath(prefix, nodeID string) string {
	if prefix == "" {
		return nodeID
	}
	if nodeID == "" {
		return prefix
	}
	return prefix + "." + nodeID
}
