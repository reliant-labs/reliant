// Copyright (c) 2025 Reliant Labs
package validation

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// walkWorkflowNodes calls fn for every node of wf and, recursively, every
// node of every inline sub-workflow — a workflow node's `inline:` body and a
// loop node's `inline:` body — with the path to that node.
//
// The per-node validation layers used to iterate wf.GetNodes() alone, so a
// node inside a loop body was validated by nobody: the structural layer
// recurses with lenient rules, the CEL layer recurses through
// validateWorkflowScope, and the cross-workflow layer recurses through
// compile contracts — but the layers that read a node's OWN configuration
// (tools_config bindings, skill references) stopped at the graph they were
// handed. A call_llm node in a loop body therefore had its tool bindings and
// skill names accepted here and discarded at run time, exactly the silent
// failure those layers exist to prevent.
//
// Ref-loaded workflows are deliberately NOT walked: each loaded definition
// is validated in its own right by StaticAnalysisWithOptions (that is what
// the compile queue is for), and walking them again would report the same
// child's findings twice on the parent's surface. Inline bodies have no such
// separate surface — they exist only inside the graph that contains them.
//
// An inline body is a literal subtree, so the walk always terminates.
//
// Every path handed to fn has len == cap, so a caller that appends to it
// always allocates: Result keeps error paths by reference, and two findings
// sharing a backing array would silently rewrite each other's paths.
func walkWorkflowNodes(wf *reliantv1.Workflow, basePath []string, fn func(node *reliantv1.Node, path []string)) {
	if wf == nil {
		return
	}
	for i, node := range wf.GetNodes() {
		if node == nil {
			continue
		}
		nodePath := joinPath(basePath, "nodes", nodePathSegment(node, i))
		fn(node, nodePath)

		if wfArgs := node.GetWorkflow(); wfArgs != nil && wfArgs.GetInline() != nil {
			walkWorkflowNodes(wfArgs.GetInline(), joinPath(nodePath, "inline"), fn)
		}
		if loopArgs := node.GetLoop(); loopArgs != nil && loopArgs.GetInline() != nil {
			walkWorkflowNodes(loopArgs.GetInline(), joinPath(nodePath, "inline"), fn)
		}
	}
}

// joinPath builds a fresh path from base plus elems, with len == cap so any
// later append allocates rather than writing into a slice a stored Error
// still points at.
func joinPath(base []string, elems ...string) []string {
	out := make([]string, 0, len(base)+len(elems))
	out = append(out, base...)
	out = append(out, elems...)
	return out
}
