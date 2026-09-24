// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// workflowCheck is the outcome of validating a workflow definition the way
// the runtime will when it is run: the same static analysis, with the user's
// workflow loader so `ref:` outputs type-check against the real child.
type workflowCheck struct {
	// parseErr is set when the definition could not be parsed at all.
	parseErr error
	result   *validation.Result
}

func (c workflowCheck) valid() bool {
	return c.parseErr == nil && (c.result == nil || !c.result.HasErrors())
}

// protoErrors renders every error (and, with warnings=true, every warning) as
// structured ValidationErrors. Warnings carry type "warning:<category>" so a
// client can tell them apart without a proto change.
func (c workflowCheck) protoErrors(warnings bool) []*reliantv1.ValidationError {
	if c.parseErr != nil {
		return []*reliantv1.ValidationError{{Type: "conversion_error", Message: c.parseErr.Error()}}
	}
	if c.result == nil {
		return nil
	}
	var out []*reliantv1.ValidationError
	for _, e := range c.result.Errors() {
		out = append(out, &reliantv1.ValidationError{
			Type:       string(e.Category),
			Message:    e.Error(),
			Suggestion: e.Suggestion,
		})
	}
	if warnings {
		for _, w := range c.result.Warnings() {
			out = append(out, &reliantv1.ValidationError{
				Type:       "warning:" + string(w.Category),
				Message:    w.Error(),
				Suggestion: w.Suggestion,
			})
		}
	}
	return out
}

// summary is a one-line description of the errors for a response message.
func (c workflowCheck) summary() string {
	if c.parseErr != nil {
		return c.parseErr.Error()
	}
	if c.result == nil || !c.result.HasErrors() {
		return ""
	}
	errs := c.result.Errors()
	first := errs[0].Error()
	if len(errs) == 1 {
		return first
	}
	return fmt.Sprintf("%s (and %d more)", first, len(errs)-1)
}

// validateWorkflowDefinition validates a YAML definition with the user's
// workflow loader.
func (s *WorkflowService) validateWorkflowDefinition(ctx context.Context, userID string, definition []byte) workflowCheck {
	self, err := wfyaml.ParseWorkflow(definition)
	if err != nil {
		return workflowCheck{parseErr: fmt.Errorf("failed to parse workflow: %w", err)}
	}
	result, err := v2.ValidateYAMLResult(definition, s.createValidationWorkflowLoader(ctx, userID, self))
	if err != nil {
		return workflowCheck{parseErr: err}
	}
	return workflowCheck{result: result}
}

// errorCount is the number of validation errors (a parse failure counts as one).
func (c workflowCheck) errorCount() int {
	if c.parseErr != nil {
		return 1
	}
	if c.result == nil {
		return 0
	}
	return len(c.result.Errors())
}

// completeRejectedMessage is the response message when validation blocks a
// workflow from being (or staying) complete. Nothing is stored: a complete
// workflow is runnable, and run start rejects the same errors.
func completeRejectedMessage(c workflowCheck) string {
	return "Workflow not saved: a complete workflow must pass validation — fix the errors or save it as a draft. " + strings.TrimSpace(c.summary())
}

// resolveDraftStatus decides the status a save stores. An explicit intent
// wins; otherwise an existing workflow keeps its status (so re-saving a
// complete workflow stays gated) and a new one starts as a draft.
func resolveDraftStatus(requested reliantv1.WorkflowDraftStatus, existing *db.WorkflowDraft) db.WorkflowDraftStatus {
	switch requested {
	case reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE:
		return db.WorkflowDraftStatusComplete
	case reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT:
		return db.WorkflowDraftStatusDraft
	}
	if existing != nil && existing.Status == db.WorkflowDraftStatusComplete {
		return db.WorkflowDraftStatusComplete
	}
	return db.WorkflowDraftStatusDraft
}

// draftStatusToProto maps a stored status onto the wire enum.
func draftStatusToProto(status db.WorkflowDraftStatus) reliantv1.WorkflowDraftStatus {
	if status == db.WorkflowDraftStatusComplete {
		return reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE
	}
	return reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT
}

// draftSavedMessage is the success message for a stored save. A draft with
// errors says what stands between it and being runnable.
func draftSavedMessage(status db.WorkflowDraftStatus, c workflowCheck) string {
	if status == db.WorkflowDraftStatusComplete {
		return "Workflow saved successfully"
	}
	if n := c.errorCount(); n > 0 {
		return fmt.Sprintf("Saved as draft with %d validation error(s) — fix them before marking it complete", n)
	}
	return "Saved as draft — mark it complete to make it runnable"
}
