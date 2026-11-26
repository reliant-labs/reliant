// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateWorkflowTimeout checks the run timeout (Workflow.timeout): a literal
// duration, more than zero and at most model.MaxWorkflowTimeout. A node whose
// own literal timeout is at least the run's can never reach its own: the run
// is stopped first. That is a warning, not an error: a generous node timeout
// beside a tighter run bound is a legitimate belt-and-braces.
func validateWorkflowTimeout(wf *reliantv1.Workflow, result *Result) {
	timeout, _, set, err := model.WorkflowTimeout(wf)
	if !set {
		return
	}
	if err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "timeout", err.Error(),
			fmt.Sprintf("a Go duration such as 3h or 90m, at most %s; omit it for no bound", model.MaxWorkflowTimeout))
		return
	}
	walkNodeTimeouts(wf.GetNodes(), []string{wf.GetName(), "nodes"}, func(node *reliantv1.Node, path []string) {
		cs := node.GetTimeout()
		if !model.CelStringIsSet(cs) || model.CelStringIsExpr(cs) {
			return
		}
		d, err := time.ParseDuration(model.CelStringValue(cs))
		if err != nil || d < timeout {
			return
		}
		result.AddWarning(CategoryStructure, path, "timeout",
			fmt.Sprintf("node %q's timeout (%s) is not shorter than the workflow's run timeout (%s): the run is stopped before the node's own timeout can end it",
				node.GetId(), d, timeout))
	})
}

// walkNodeTimeouts visits every node of a graph, through inline sub-workflow
// and loop bodies, with its path.
func walkNodeTimeouts(nodes []*reliantv1.Node, base []string, visit func(*reliantv1.Node, []string)) {
	for i, node := range nodes {
		path := append(append([]string{}, base...), fmt.Sprintf("[%d](%s)", i, node.GetId()))
		visit(node, path)
		var inline *reliantv1.Workflow
		if args := node.GetWorkflow(); args != nil {
			inline = args.GetInline()
		} else if args := node.GetLoop(); args != nil {
			inline = args.GetInline()
		}
		if inline != nil {
			walkNodeTimeouts(inline.GetNodes(), append(append([]string{}, path...), "inline", "nodes"), visit)
		}
	}
}
