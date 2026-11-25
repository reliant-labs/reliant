// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A `type: review` node never reaches an executor as itself: lowerForExecution
// compiles it, at load, into a workflow node that runs model.ReviewAgentRef in
// a new thread (model.LowerReviewNode). The inline executor that runs that
// node does the rest, at the three points a review differs from any other
// sub-workflow:
//
//  1. its session is a reviewer's (reviewerSession), so every call_llm in it
//     is narrowed to model.ReviewerTools before it is dispatched
//     (clampReviewerStep);
//  2. its output is the reviewer's verdict, checked (reviewNodeOutputs): a
//     missing or malformed one fails the node;
//  3. the output records the session (the review thread) and the role, so a
//     server action can check that the verdict came from a reviewer whose
//     session never pushed to the change it judged.

// reviewerSession returns ctx marked as a reviewer session. A copy: the
// caller's context is shared with the node's siblings.
func reviewerSession(ctx *ExecutionContext) *ExecutionContext {
	if ctx == nil {
		return nil
	}
	marked := *ctx
	marked.Reviewer = true
	return &marked
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
