// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"gopkg.in/yaml.v3"
)

// workflowToolCheck is the outcome of validating workflow YAML for the
// workflow-editing tools, the same way run start will.
type workflowToolCheck struct {
	// syntaxErr is set when the content is not YAML or not a workflow.
	syntaxErr error
	result    *validation.Result
}

func (c workflowToolCheck) hasErrors() bool {
	return c.syntaxErr != nil || (c.result != nil && c.result.HasErrors())
}

// validateWorkflowForTool validates content with a workflow loader, so `ref:`
// children resolve (builtins from the embedded catalog, the user's own drafts
// from the repo) and their outputs type-check. Without the loader a ref's
// outputs are dyn, which is weaker than the run-start validation the
// workflow will later face: the agent would be told "valid" and meet the
// error only when someone runs it.
func validateWorkflowForTool(ctx context.Context, repo db.Repository, content string) workflowToolCheck {
	var raw interface{}
	if err := yaml.Unmarshal([]byte(content), &raw); err != nil {
		return workflowToolCheck{syntaxErr: fmt.Errorf("invalid YAML syntax: %w", err)}
	}
	result, err := v2.ValidateYAMLResult([]byte(content), workflowToolLoader(ctx, repo))
	if err != nil {
		return workflowToolCheck{syntaxErr: fmt.Errorf("workflow could not be parsed: %w", err)}
	}
	return workflowToolCheck{result: result}
}

// workflowToolLoader resolves refs for validation: builtin:// from the
// embedded catalog, anything else as one of the caller's drafts. A ref that
// cannot be resolved returns (nil, nil) so validation continues — structural
// validation reports unknown refs itself.
func workflowToolLoader(ctx context.Context, repo db.Repository) v2.WorkflowLoader {
	userID, _ := auth.GetUserIDFromContext(ctx)
	return func(ref string) (*reliantv1.Workflow, error) {
		if strings.HasPrefix(ref, "builtin://") {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(strings.TrimPrefix(ref, "builtin://") + ".yaml")
			if err != nil {
				return nil, nil
			}
			return wfyaml.ParseWorkflow(data)
		}
		if repo == nil || userID == "" {
			return nil, nil
		}
		slug := generateSlugFromName(ref)
		if slug == "" {
			return nil, nil
		}
		draft, err := repo.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err != nil || draft == nil || draft.Definition == "" {
			return nil, nil
		}
		return wfyaml.ParseWorkflow([]byte(draft.Definition))
	}
}

// rejection is the tool response text for content validation blocked: every
// error with its fix, and a reminder that nothing was saved.
func (c workflowToolCheck) rejection(action string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workflow NOT %s — it has validation errors. Nothing was saved; fix these and call the tool again.\n\n", action)
	if c.syntaxErr != nil {
		b.WriteString("- " + c.syntaxErr.Error() + "\n")
		return b.String()
	}
	for _, e := range c.result.Errors() {
		b.WriteString("- " + e.Error() + "\n")
	}
	if w := c.warningsText(); w != "" {
		b.WriteString("\n" + w)
	}
	return b.String()
}

// warningsText lists every warning, or "" when there are none. Warnings do
// not block a save, but they are the findings most predictive of a run-time
// surprise (a stand-in zero read as a real value, a loosely-typed schema),
// so the agent is always shown them.
func (c workflowToolCheck) warningsText() string {
	if c.result == nil || !c.result.HasWarnings() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Warnings (saved anyway; worth fixing):\n")
	for _, w := range c.result.Warnings() {
		b.WriteString("- " + w.Error() + "\n")
	}
	return b.String()
}

// withWarnings appends the warnings to a success message.
func (c workflowToolCheck) withWarnings(msg string) string {
	if w := c.warningsText(); w != "" {
		return msg + "\n\n" + w
	}
	return msg
}
