// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gitpublish"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateGitPublishNode checks a git_publish node's declaration. What can be
// known without running is checked here: the required arguments, that exactly
// one of branch and pr_head_of names the target, and that a LITERAL branch is
// one the publish policy could ever accept. The repository's default and
// protected branches are GitHub's to say and are checked when the node runs.
func validateGitPublishNode(node *reliantv1.Node, nodePath []string, result *Result) {
	args := node.GetGitPublish()
	if args == nil {
		result.AddError(CategoryStructure, nodePath, "args", "git_publish node missing args")
		return
	}
	for _, f := range []struct {
		field string
		set   bool
	}{
		{"worktree", model.CelStringIsSet(args.GetWorktree())},
		{"repo", model.CelStringIsSet(args.GetRepo())},
		{"message", model.CelStringIsSet(args.GetMessage())},
	} {
		if !f.set {
			result.AddError(CategoryStructure, nodePath, f.field, "git_publish needs a "+f.field+" (required)")
		}
	}

	hasBranch := model.CelStringIsSet(args.GetBranch())
	hasPR := args.GetPrHeadOf() != nil && args.GetPrHeadOf().GetValue() != nil
	switch {
	case hasBranch && hasPR:
		result.AddError(CategoryStructure, nodePath, "branch", "set either branch or pr_head_of, not both")
	case !hasBranch && !hasPR:
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "branch", "git_publish needs a branch or a pr_head_of",
			`branch: "autopilot/issue-{{ inputs.issue }}"   # or pr_head_of: "{{ inputs.pr }}"`)
	}

	if hasBranch && !model.CelStringIsExpr(args.GetBranch()) {
		branch := strings.TrimSpace(model.CelStringValue(args.GetBranch()))
		switch {
		case !gitpublish.ValidBranch(branch):
			result.AddError(CategoryStructure, nodePath, "branch",
				"not a plain branch name (letters, digits and . _ / - only; no refs/ prefix, no tags)")
		case gitpublish.NeverPublishable(branch):
			result.AddError(CategoryStructure, nodePath, "branch",
				"main, master and release/* are never published to: choose a feature branch")
		}
	}

	if allow := args.GetAllow(); allow != nil && !model.CelStringListIsExpr(allow) {
		for _, p := range model.CelStringListValue(allow) {
			if strings.TrimSpace(p) == "*" {
				result.AddWarning(CategoryStructure, nodePath, "allow",
					`"*" allows every branch the built-in checks accept; name a prefix such as "autopilot/*"`)
			}
		}
	}
}
