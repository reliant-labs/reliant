// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/attention"
)

// Validate every embedded definition even though only a root unattended launch
// applies attention. Direct proto admission must not bypass the YAML checks.
func validateAttentionTree(wf *reliantv1.Workflow, result *Result) {
	seen := map[*reliantv1.Workflow]bool{}
	var visit func(*reliantv1.Workflow, []string)
	visit = func(wf *reliantv1.Workflow, path []string) {
		if wf == nil || seen[wf] {
			return
		}
		seen[wf] = true
		if err := attention.Validate(wf.GetAttention()); err != nil {
			result.AddError(CategoryStructure, path, "attention", err.Error())
		}
		visit(wf.GetOnFailure(), append(append([]string{}, path...), "on_failure"))
		for i, node := range wf.GetNodes() {
			nodePath := append(append([]string{}, path...), "nodes", fmt.Sprintf("[%d](%s)", i, node.GetId()))
			if args := node.GetWorkflow(); args != nil {
				visit(args.GetInline(), append(nodePath, "inline"))
			}
			if args := node.GetLoop(); args != nil {
				visit(args.GetInline(), append(nodePath, "inline"))
			}
		}
	}
	visit(wf, nil)
}
