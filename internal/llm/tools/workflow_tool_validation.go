// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"errors"
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
	self, err := wfyaml.ParseWorkflow([]byte(content))
	if err != nil {
		return workflowToolCheck{syntaxErr: fmt.Errorf("workflow could not be parsed: %w", err)}
	}
	result, err := v2.ValidateYAMLResult([]byte(content), workflowToolLoader(ctx, repo, self))
	if err != nil {
		return workflowToolCheck{syntaxErr: fmt.Errorf("workflow could not be parsed: %w", err)}
	}
	return workflowToolCheck{result: result}
}

// workflowToolLoader resolves refs for validation: builtin:// from the
// embedded catalog, anything else as one of the caller's complete workflows.
// A ref that cannot be resolved returns (nil, nil) so validation continues —
// structural validation reports unknown refs itself. The workflow being
// validated resolves to itself, so self-spawning works while it is a draft.
func workflowToolLoader(ctx context.Context, repo db.Repository, self *reliantv1.Workflow) v2.WorkflowLoader {
	userID, _ := auth.GetUserIDFromContext(ctx)
	selfSlug := ""
	if self != nil {
		selfSlug = generateSlugFromName(self.GetName())
	}
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
		if slug == selfSlug {
			return self, nil
		}
		// Resolve refs exactly as run start does: only a complete workflow
		// loads. A draft child is an error here too, so "valid" never means
		// "valid until someone runs it".
		draft, err := repo.GetUsableWorkflowBySlug(ctx, userID, slug)
		if err != nil {
			var notRunnable *db.WorkflowDraftNotRunnableError
			if errors.As(err, &notRunnable) {
				return nil, err
			}
			return nil, nil
		}
		if draft == nil || draft.Definition == "" {
			return nil, nil
		}
		return wfyaml.ParseWorkflow([]byte(draft.Definition))
	}
}

// rejection is the tool response text for a write validation blocked: every
// error, and a reminder that nothing was saved. reason says why the errors
// block (the content does not parse, or the workflow is/would be complete).
func (c workflowToolCheck) rejection(action, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workflow NOT %s — %s Nothing was saved.\n\n", action, reason)
	b.WriteString(c.errorsText())
	if w := c.warningsText(); w != "" {
		b.WriteString("\n" + w)
	}
	return b.String()
}

// errorsText lists every error, or "" when there are none.
func (c workflowToolCheck) errorsText() string {
	if c.syntaxErr != nil {
		return "Errors:\n- " + c.syntaxErr.Error() + "\n"
	}
	if c.result == nil || !c.result.HasErrors() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Errors:\n")
	for _, e := range c.result.Errors() {
		b.WriteString("- " + e.Error() + "\n")
	}
	return b.String()
}

// completeRejectionReason explains why errors block a write that leaves the
// workflow complete, and how to keep iterating instead.
const completeRejectionReason = "a complete workflow must pass validation, and this content has errors. " +
	"Fix them, or pass complete: false to save it as a draft (drafts do not run)."

// syntaxRejectionReason explains a rejection even a draft cannot avoid.
const syntaxRejectionReason = "the content could not be parsed as a workflow, so there is nothing to store, even as a draft."

// resolveToolStatus decides the status a tool write stores: an explicit
// `complete` wins; otherwise an existing workflow keeps its status (so an
// agent never silently takes a runnable workflow out of service) and a new
// one starts as a draft.
func resolveToolStatus(complete *bool, existing *db.WorkflowDraft) db.WorkflowDraftStatus {
	if complete != nil {
		if *complete {
			return db.WorkflowDraftStatusComplete
		}
		return db.WorkflowDraftStatusDraft
	}
	if existing != nil && existing.Status == db.WorkflowDraftStatusComplete {
		return db.WorkflowDraftStatusComplete
	}
	return db.WorkflowDraftStatusDraft
}

// gate reports the rejection text for storing content with this status, or
// "" when the write may proceed.
func (c workflowToolCheck) gate(action string, status db.WorkflowDraftStatus) string {
	if c.syntaxErr != nil {
		return c.rejection(action, syntaxRejectionReason)
	}
	if status == db.WorkflowDraftStatusComplete && c.hasErrors() {
		return c.rejection(action, completeRejectionReason)
	}
	return ""
}

// outcome appends the resulting status and every current finding to a
// success message, so the agent always knows whether it is done.
func (c workflowToolCheck) outcome(msg string, status db.WorkflowDraftStatus) string {
	var b strings.Builder
	b.WriteString(msg)
	b.WriteString("\n\n")
	switch {
	case status == db.WorkflowDraftStatusComplete:
		b.WriteString("Status: complete (runnable).\n")
	case c.hasErrors():
		b.WriteString("Status: draft — NOT runnable. Fix the errors below, then save with complete: true.\n")
	default:
		b.WriteString("Status: draft — valid, but NOT runnable until marked complete (save with complete: true).\n")
	}
	if e := c.errorsText(); e != "" {
		b.WriteString("\n" + e)
	}
	if w := c.warningsText(); w != "" {
		b.WriteString("\n" + w)
	}
	return strings.TrimRight(b.String(), "\n")
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
	b.WriteString("Warnings (do not block; worth fixing):\n")
	for _, w := range c.result.Warnings() {
		b.WriteString("- " + w.Error() + "\n")
	}
	return b.String()
}
