package validation

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateNodeRole holds Node.role to its closed set and to the one node kind
// it means anything on. A role is what a server action trusts (a verdict is
// accepted only from a node declared role: reviewer), so a role on any other
// node is an error, never ignored: ignoring it would let a definition look
// like it carries a reviewer it does not have.
func validateNodeRole(node *reliantv1.Node, nodePath []string, result *Result) {
	role := node.GetRole()
	isReview := node.GetType() == model.NodeTypeReview
	switch {
	case role == "" && isReview:
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "role",
			"a review node requires role: "+model.RoleReviewer,
			"role: "+model.RoleReviewer)
	case role == "":
	case !model.IsValidRole(role):
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "role",
			fmt.Sprintf("unknown role %q", role),
			fmt.Sprintf("valid roles are: %s", strings.Join(model.ValidRoles, ", ")))
	case !isReview:
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "role",
			fmt.Sprintf("role is only valid on a review node, not on a %s node", node.GetType()),
			"use type: review for a reviewer, or remove role")
	}
}

// validateReviewNode checks what a review node may configure: its prompt, its
// data, its model and its turn bound. Its tools, session and agent are the
// engine's and have no fields to set (the parser refuses those keys by name).
func validateReviewNode(node *reliantv1.Node, nodePath []string, result *Result) {
	args := node.GetReview()
	if args == nil {
		result.AddError(CategoryStructure, nodePath, "args", "review node missing args")
		return
	}

	prompt := args.GetPrompt()
	switch {
	case !model.CelStringIsSet(prompt) || strings.TrimSpace(model.CelStringRaw(prompt)) == "":
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "prompt", "a review node requires a prompt",
			`prompt: builtin://pipeline/review.md   # or inline text`)
	case model.CelStringIsExpr(prompt):
		// A template is inline text once it is evaluated.
	case model.IsBuiltinPromptRef(model.CelStringValue(prompt)):
		ref := strings.TrimSpace(model.CelStringValue(prompt))
		if _, err := builtin.PromptFile(strings.TrimPrefix(ref, model.BuiltinPromptScheme)); err != nil {
			result.AddError(CategoryStructure, nodePath, "prompt", fmt.Sprintf("prompt %s: %v", ref, err))
		}
	case looksLikePromptPath(model.CelStringValue(prompt)):
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "prompt",
			fmt.Sprintf("prompt %q looks like a file path: a review prompt is inline text or a builtin:// prompt, never a file in the repository under review", strings.TrimSpace(model.CelStringValue(prompt))),
			"inline the text, or use a prompt the engine ships (builtin://<group>/<name>.md)")
	}

	if mt := args.GetMaxTurns(); model.CelIntIsSet(mt) && !model.CelIntIsExpr(mt) {
		if n := model.CelIntValue(mt); n < 1 || n > 500 {
			result.AddError(CategoryStructure, nodePath, "max_turns", fmt.Sprintf("max_turns must be between 1 and 500, got %d", n))
		}
	}

	if m := args.GetModel(); model.CelModelSelectorIsSet(m) && !model.CelModelSelectorIsExpr(m) {
		if ms := model.CelModelSelectorValue(m); ms != nil && ms.GetId() == "" && len(ms.GetTags()) == 0 {
			result.AddError(CategoryStructure, nodePath, "model", "model must have 'id' or 'tags' set")
		}
	}
}

// looksLikePromptPath reports whether a literal prompt is a single token that
// names a file or a non-builtin URL (".reliant/prompts/review.md",
// "project://review.md") rather than prose.
func looksLikePromptPath(prompt string) bool {
	p := strings.TrimSpace(prompt)
	if p == "" || strings.ContainsAny(p, " \t\n") {
		return false
	}
	if strings.Contains(p, "://") {
		return true
	}
	for _, ext := range []string{".md", ".txt", ".prompt"} {
		if strings.HasSuffix(strings.ToLower(p), ext) {
			return true
		}
	}
	return false
}
