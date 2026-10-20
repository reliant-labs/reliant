// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// on_failure: the handler is an inline graph that runs once, after the main
// graph FAILED, with the main graph's node outputs as they stood and a `failure`
// namespace. Validation holds it to the three promises that make that safe:
//
//   - Its nodes cannot be mistaken for the main graph's. A shared id would make
//     `nodes.<id>` mean the main node in one place and the handler's in another.
//   - It cannot wait for a person. The run is already failed and nobody is
//     answering; an approval or question would hold a FAILED run open forever.
//   - It reads the main graph only defensively. Whichever node failed, the nodes
//     after it never ran, so an unguarded `nodes.later.x` fails the handler at
//     the moment it is needed. The execution-order analysis already knows how to
//     say that; the handler is checked as a graph in which NO main node is
//     guaranteed to have run.

const onFailureField = "on_failure"

// validateOnFailureStructure checks the handler's shape and its ids against the
// main graph's.
func validateOnFailureStructure(wf *reliantv1.Workflow, mainIDs map[string]int, result *Result) {
	handler := wf.GetOnFailure()
	if handler == nil {
		return
	}
	base := []string{wf.GetName(), onFailureField}

	if _, taken := mainIDs[onFailureField]; taken {
		result.AddError(CategoryStructure, []string{wf.GetName(), "nodes"}, "id",
			"node id 'on_failure' is reserved for the on_failure handler's path in scenarios and run records")
	}
	if handler.GetOnFailure() != nil {
		result.AddError(CategoryStructure, base, onFailureField,
			"an on_failure handler cannot declare its own on_failure: a handler's failure is logged, never handled")
	}
	if len(handler.GetOutputs()) > 0 {
		result.AddError(CategoryStructure, base, "outputs",
			"an on_failure handler has no outputs: nothing reads them, the run ends failed")
	}
	if len(handler.GetInputs()) > 0 {
		result.AddError(CategoryStructure, base, "inputs",
			"an on_failure handler shares the workflow's inputs; declare them on the workflow")
	}
	if len(handler.GetNodes()) == 0 {
		result.AddError(CategoryStructure, base, "nodes", "on_failure must have at least one node")
		return
	}

	validateInlineWorkflow(handler, base, result)

	for i, node := range handler.GetNodes() {
		path := append(append([]string{}, base...), "nodes", fmt.Sprintf("[%d](%s)", i, node.GetId()))
		if _, clash := mainIDs[node.GetId()]; clash {
			result.AddErrorWithSuggestion(CategoryStructure, path, "id",
				fmt.Sprintf("node id '%s' is also a node of the main graph; inside on_failure, nodes.%s would be ambiguous", node.GetId(), node.GetId()),
				"give the handler's node its own id")
		}
		rejectWaitingNodes(node, path, result)
	}
}

// rejectWaitingNodes refuses nodes that wait for a person, at any depth.
func rejectWaitingNodes(node *reliantv1.Node, path []string, result *Result) {
	switch node.GetType() {
	case model.NodeTypeApproval, model.NodeTypeAskQuestion:
		result.AddErrorWithSuggestion(CategoryStructure, path, "type",
			fmt.Sprintf("a %s node cannot be part of on_failure: the run has already failed and nobody is answering", node.GetType()),
			"report the failure (a save_message or an action) instead of asking about it")
		return
	}
	var inline *reliantv1.Workflow
	if args := node.GetWorkflow(); args != nil {
		inline = args.GetInline()
	} else if args := node.GetLoop(); args != nil {
		inline = args.GetInline()
	}
	for i, child := range inline.GetNodes() {
		rejectWaitingNodes(child, append(append([]string{}, path...), "inline", "nodes", fmt.Sprintf("[%d](%s)", i, child.GetId())), result)
	}
}

// validateOnFailureCEL compiles the handler's expressions in a scope that has
// the `failure` namespace, the workflow's inputs, and every main-graph node's
// type, with none of them guaranteed to have produced an output.
func validateOnFailureCEL(wf *reliantv1.Workflow, mainCtx *WorkflowTypeContext, result *Result) {
	handler := wf.GetOnFailure()
	if handler == nil || mainCtx == nil || len(handler.GetNodes()) == 0 {
		return
	}

	// A copy of the main graph's type context, extended with the handler's own
	// nodes. The main context is not modified: the main graph was already
	// validated against it.
	ctx := *mainCtx
	ctx.NodeTypes = copyStringMap(mainCtx.NodeTypes)
	ctx.NodeOutputs = copyOutputsMap(mainCtx.NodeOutputs)
	ctx.ConditionalNodes = copyStringMap(mainCtx.ConditionalNodes)
	own := BuildWorkflowTypeContext(handler, nil)
	if own != nil {
		for id, t := range own.NodeTypes {
			ctx.NodeTypes[id] = t
		}
		for id, out := range own.NodeOutputs {
			ctx.NodeOutputs[id] = out
		}
		for id, cond := range own.ConditionalNodes {
			ctx.ConditionalNodes[id] = cond
		}
		ctx.ResponseTools = mergeResponseTools(mainCtx.ResponseTools, own.ResponseTools)
		ctx.NodesWithExtendedOutputs = mergeBoolMaps(mainCtx.NodesWithExtendedOutputs, own.NodesWithExtendedOutputs)
	}

	scope := newWorkflowScope(handler, []string{wf.GetName(), onFailureField}, &ctx, nil)
	scope.failure = true
	// Guaranteed-before is the handler's own graph only: no main node is
	// guaranteed to have run, which is what makes an unguarded read an error.
	for _, n := range wf.GetNodes() {
		scope.nodeIDs = append(scope.nodeIDs, n.GetId())
	}
	sort.Strings(scope.nodeIDs)
	validateWorkflowScope(scope, result)
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyOutputsMap(in map[string]map[string]*FieldInfo) map[string]map[string]*FieldInfo {
	out := make(map[string]map[string]*FieldInfo, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeBoolMaps(a, b map[string]bool) map[string]bool {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]bool, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func mergeResponseTools(a, b *ResponseToolContext) *ResponseToolContext {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := &ResponseToolContext{
		AvailableTools:  map[string]map[string]*ResponseToolSchema{},
		ToolCallSources: map[string]ToolCallSource{},
	}
	for _, src := range []*ResponseToolContext{a, b} {
		for k, v := range src.AvailableTools {
			out.AvailableTools[k] = v
		}
		for k, v := range src.ToolCallSources {
			out.ToolCallSources[k] = v
		}
	}
	return out
}
