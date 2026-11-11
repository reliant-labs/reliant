// Copyright (c) 2025 Reliant Labs
package validation

import (
	"slices"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validatePermissions checks the declared `permissions:`: known values, none
// twice. The YAML parser reads the list; this holds proto and builder callers
// to the same names.
func validatePermissions(wf *reliantv1.Workflow, result *Result) {
	seen := map[string]bool{}
	for _, p := range wf.GetPermissions() {
		switch {
		case !slices.Contains(model.WorkflowPermissions, p):
			result.AddErrorWithSuggestion(CategoryStructure, nil, "permissions",
				"unknown permission "+quote(p),
				"the only permission is "+strings.Join(model.WorkflowPermissions, ", "))
		case seen[p]:
			result.AddError(CategoryStructure, nil, "permissions", quote(p)+" is declared twice")
		}
		seen[p] = true
	}
}

func quote(s string) string { return `"` + s + `"` }
