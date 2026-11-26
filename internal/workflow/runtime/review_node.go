// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A `type: review` node never reaches an executor as itself: lowerForExecution
// compiles it, at load, into a workflow node that runs model.ReviewAgentRef in
// a new thread (model.LowerReviewNode). The runtime does the rest, at the four
// points a review differs from any other sub-workflow:
//
//  1. its context is a reviewer session (reviewSession), derived where every
//     workflow node's context is (workflowNodeContext): before its thread is
//     created or its body is built, on a thread of its own for THIS
//     execution;
//  2. every call_llm in that session is narrowed to model.ReviewerTools
//     before it is dispatched (clampReviewerStep);
//  3. its output is the reviewer's verdict, checked (reviewNodeOutputs): a
//     missing or malformed one fails the node;
//  4. the output records the session (the review thread) and the role, so a
//     server action can check that the verdict came from a reviewer whose
//     session never pushed to the change it judged.

// workflowNodeContext is parent.ForChild for the body of workflow node node,
// which sits at nodePath. A review node's body gets a reviewer session of its
// own for this execution (reviewSession) instead of the per-node thread
// ForChild derives. Every executor that runs a workflow node derives the
// body's context here.
func workflowNodeContext(
	wctx workflow.Context,
	tracker *ChildWorkflowTracker,
	parent *ExecutionContext,
	node *reliantv1.Node,
	nodePath, mode, workflowName string,
	memo bool,
) *ExecutionContext {
	child := parent.ForChild(node.GetId(), mode, workflowName, memo)
	if model.IsLoweredReviewNode(node) {
		child = reviewSession(wctx, tracker, child, nodePath)
	}
	return child
}

// reviewSession turns the context ForChild derived for a review node's body
// into a reviewer session: marked Reviewer, so every model call in it is held
// to the reviewer's tools, and on a thread no other execution has.
//
// The reviewer gets a fresh session every round (owner, 2026-11-25):
// independence over memory, so what earlier rounds found reaches it only as
// data in its brief (`with:`), never as session history. ForChild keys a new
// thread on the node id and the innermost loop's iteration, so a review node
// in an outer loop's body, or in a sub-workflow a loop runs, would get the
// same thread every round. This one is keyed on every fact that tells one
// execution from another, each of them workflow state that replays
// identically:
//
//   - the run: workflow.Info's OriginalRunID. A chat's root workflow id is
//     the chat id, so every run in a chat shares it, and the run id tells
//     them apart. OriginalRunID, unlike the current run id, survives a reset,
//     which re-executes the history it keeps;
//   - where the node is: its node path, and the iteration of every loop
//     around it (LoopPath);
//   - an ordinal: how many times this run executed the node at that position
//     before (ChildWorkflowTracker.reviewExecutions). An execution at one
//     position follows the last one's end, so the count runs in an order a
//     replay reproduces; it tells apart any re-entry the position cannot.
//
// A copy: the caller's context is its own.
func reviewSession(wctx workflow.Context, tracker *ChildWorkflowTracker, child *ExecutionContext, nodePath string) *ExecutionContext {
	if child == nil {
		return nil
	}
	session := *child
	session.Reviewer = true
	runID := ""
	if wctx != nil {
		runID = workflow.GetInfo(wctx).OriginalRunID
	}
	ordinal := tracker.nextReviewExecution(child.WorkflowID, nodePath, child.LoopPath)
	session.Thread = reviewSessionThread(child.WorkflowID, runID, nodePath, child.LoopPath, ordinal)
	return &session
}

// reviewSessionThread is the thread of one execution of a review node.
func reviewSessionThread(workflowID, runID, nodePath, loopPath string, ordinal int) string {
	return DeterministicThread(workflowID, fmt.Sprintf("%s:review:run:%s:loop:%s:exec:%d", nodePath, runID, loopPath, ordinal))
}

// nextReviewExecution is the ordinal of this execution of the review node at
// nodePath, in loop iteration loopPath of workflow workflowID: 0 the first
// time this run executes it there, then 1, 2, ... A nil tracker (a test that
// builds no run) counts nothing.
func (t *ChildWorkflowTracker) nextReviewExecution(workflowID, nodePath, loopPath string) int {
	if t == nil {
		return 0
	}
	if t.reviewExecutions == nil {
		t.reviewExecutions = make(map[string]int)
	}
	key := workflowID + "\x00" + nodePath + "\x00" + loopPath
	n := t.reviewExecutions[key]
	t.reviewExecutions[key] = n + 1
	return n
}

// reviewNodeOutputs turns the review body's outputs (its submitted response)
// into the review node's output (model.BuildReviewOutput), recording the
// session it ran in.
func reviewNodeOutputs(outputs map[string]interface{}, ctx *ExecutionContext) (map[string]interface{}, error) {
	session := ""
	if ctx != nil {
		session = ctx.Thread
	}
	if session == "" {
		return nil, fmt.Errorf("the reviewer ran in no session of its own")
	}
	return model.BuildReviewOutput(outputs["response"], session)
}

// clampReviewerStep narrows a call_llm step in a reviewer session to the
// reviewer's tools (model.ClampReviewerTools). Every other step, and every
// step outside a reviewer session, is left alone.
func clampReviewerStep(ctx *ExecutionContext, nodeType string, evalResult *reliantv1.Node) {
	if ctx == nil || !ctx.Reviewer || nodeType != model.NodeTypeCallLLM {
		return
	}
	model.ClampReviewerTools(evalResult)
}
