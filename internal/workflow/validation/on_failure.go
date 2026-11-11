// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// on_failure and finally: each handler is an inline graph that runs once, after the main
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

const (
	onFailureField = "on_failure"
	finallyField   = "finally"
)

// handlerKind is what differs between the two terminal handlers, in the words
// validation uses for them. Everything else — the three promises above — is
// the same for both: a finally handler also runs after the main graph (it
// ended, however it ended), also has nobody answering, and also cannot assume
// any main node ran, since a failure or a cancel can stop the graph anywhere.
type handlerKind struct {
	field string
	// ended finishes "the run ...": why there is nothing to wait for.
	ended string
	// reported is the run's verdict, which the handler cannot change.
	reported string
	// a is the handler's article in messages ("an on_failure", "a finally").
	a string
	// waitSuggestion is what to do instead of waiting for a person.
	waitSuggestion string
	// failure and run are the namespaces only this handler's expressions read.
	failure, run bool
}

var (
	onFailureKind = handlerKind{field: onFailureField, a: "an", ended: "has already failed", reported: "the run ends failed",
		waitSuggestion: "report the failure (a save_message or an action) instead of asking about it", failure: true}
	finallyKind = handlerKind{field: finallyField, a: "a", ended: "has ended", reported: "the run has ended",
		waitSuggestion: "report how the run ended (a save_message or an action) instead of asking about it", failure: true, run: true}
)

func (k handlerKind) handler(wf *reliantv1.Workflow) *reliantv1.Workflow {
	if k.field == finallyField {
		return wf.GetFinally()
	}
	return wf.GetOnFailure()
}

// validateOnFailureStructure checks both handlers' shape and ids against the
// main graph's.
func validateOnFailureStructure(wf *reliantv1.Workflow, mainIDs map[string]int, result *Result) {
	validateHandlerStructure(wf, onFailureKind, mainIDs, result)
	validateHandlerStructure(wf, finallyKind, mainIDs, result)
}

func validateHandlerStructure(wf *reliantv1.Workflow, kind handlerKind, mainIDs map[string]int, result *Result) {
	handler := kind.handler(wf)
	if handler == nil {
		return
	}
	base := []string{wf.GetName(), kind.field}

	if _, taken := mainIDs[kind.field]; taken {
		result.AddError(CategoryStructure, []string{wf.GetName(), "nodes"}, "id",
			fmt.Sprintf("node id '%s' is reserved for the %s handler's path in scenarios and run records", kind.field, kind.field))
	}
	if handler.GetOnFailure() != nil {
		result.AddError(CategoryStructure, base, onFailureField,
			fmt.Sprintf("%s %s handler cannot declare its own on_failure: a handler's failure is logged, never handled", kind.a, kind.field))
	}
	if handler.GetFinally() != nil {
		result.AddError(CategoryStructure, base, finallyField,
			fmt.Sprintf("%s %s handler cannot declare its own finally: it already runs once, as the run ends", kind.a, kind.field))
	}
	if len(handler.GetOutputs()) > 0 {
		result.AddError(CategoryStructure, base, "outputs",
			fmt.Sprintf("%s %s handler has no outputs: nothing reads them, %s", kind.a, kind.field, kind.reported))
	}
	if len(handler.GetInputs()) > 0 {
		result.AddError(CategoryStructure, base, "inputs",
			fmt.Sprintf("%s %s handler shares the workflow's inputs; declare them on the workflow", kind.a, kind.field))
	}
	if len(handler.GetNodes()) == 0 {
		result.AddError(CategoryStructure, base, "nodes", fmt.Sprintf("%s must have at least one node", kind.field))
		return
	}

	validateInlineWorkflow(handler, base, result)

	for i, node := range handler.GetNodes() {
		path := append(append([]string{}, base...), "nodes", fmt.Sprintf("[%d](%s)", i, node.GetId()))
		if _, clash := mainIDs[node.GetId()]; clash {
			result.AddErrorWithSuggestion(CategoryStructure, path, "id",
				fmt.Sprintf("node id '%s' is also a node of the main graph; inside %s, nodes.%s would be ambiguous", node.GetId(), kind.field, node.GetId()),
				"give the handler's node its own id")
		}
		rejectWaitingNodes(node, kind, path, result)
	}
}

// rejectWaitingNodes refuses nodes that wait for a person, at any depth.
func rejectWaitingNodes(node *reliantv1.Node, kind handlerKind, path []string, result *Result) {
	switch node.GetType() {
	case model.NodeTypeApproval, model.NodeTypeAskQuestion:
		result.AddErrorWithSuggestion(CategoryStructure, path, "type",
			fmt.Sprintf("a %s node cannot be part of %s: the run %s and nobody is answering", node.GetType(), kind.field, kind.ended),
			kind.waitSuggestion)
		return
	}
	var inline *reliantv1.Workflow
	if args := node.GetWorkflow(); args != nil {
		inline = args.GetInline()
	} else if args := node.GetLoop(); args != nil {
		inline = args.GetInline()
	}
	for i, child := range inline.GetNodes() {
		rejectWaitingNodes(child, kind, append(append([]string{}, path...), "inline", "nodes", fmt.Sprintf("[%d](%s)", i, child.GetId())), result)
	}
}

// validateOnFailureCEL compiles both handlers' expressions.
func validateOnFailureCEL(wf *reliantv1.Workflow, mainCtx *WorkflowTypeContext, result *Result) {
	validateHandlerCEL(wf, onFailureKind, mainCtx, result)
	validateHandlerCEL(wf, finallyKind, mainCtx, result)
}

// validateHandlerCEL compiles a handler's expressions in a scope that has its
// own namespaces (`failure`; `run` too for finally), the workflow's inputs,
// and every main-graph node's type, with none of them guaranteed to have
// produced an output.
func validateHandlerCEL(wf *reliantv1.Workflow, kind handlerKind, mainCtx *WorkflowTypeContext, result *Result) {
	handler := kind.handler(wf)
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
	own := buildWorkflowTypeContext(handler, nil, mainCtx.valueTypes())
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

	scope := newWorkflowScope(handler, []string{wf.GetName(), kind.field}, &ctx, nil)
	scope.failure = kind.failure
	scope.run = kind.run
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
