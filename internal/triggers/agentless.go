package triggers

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// WorkflowRunsAgent reports whether wf may hand its seed prompt to a model: it
// has a call_llm or compact node, or a step whose body is decided elsewhere (a
// child workflow, a loop over a ref, a router), which is counted as one.
//
// An activation of a declared trigger with no prompt is refused only for such
// a workflow: an agent started with nothing to do is a mistake, but a chain of
// run and action steps (a workflow_event trigger that reacts to another run
// finishing) has nothing to say to a model, and its runs start from
// DefaultSeedPrompt.
func WorkflowRunsAgent(wf *reliantv1.Workflow) bool {
	return workflowRunsAgent(wf, 0)
}

func workflowRunsAgent(wf *reliantv1.Workflow, depth int) bool {
	if wf == nil {
		return false
	}
	if depth > 16 {
		return true
	}
	for _, n := range wf.GetNodes() {
		switch model.NodeType(n) {
		case model.NodeTypeCallLLM, model.NodeTypeWorkflow, model.NodeTypeReview, model.NodeTypeCompact, model.NodeTypeRouter:
			return true
		case model.NodeTypeLoop:
			la := model.GetLoopArgs(n)
			if la.GetInline() == nil || model.CelStringIsSet(la.GetRef()) || workflowRunsAgent(la.GetInline(), depth+1) {
				return true
			}
		}
	}
	return workflowRunsAgent(wf.GetOnFailure(), depth+1) || workflowRunsAgent(wf.GetFinally(), depth+1)
}
