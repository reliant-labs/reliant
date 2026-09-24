// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
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
	result, err := v2.ValidateYAMLResult(definition, s.createValidationWorkflowLoader(ctx, userID))
	if err != nil {
		return workflowCheck{parseErr: err}
	}
	return workflowCheck{result: result}
}

// saveRejectedMessage is the response message for a save that validation
// blocked. The draft is NOT persisted: a stored draft is runnable by `ref:`
// and by chats, and run start rejects the same errors — saving it would only
// defer the failure to the moment someone runs it.
func saveRejectedMessage(c workflowCheck) string {
	return "Workflow not saved: fix the validation errors first — " + strings.TrimSpace(c.summary())
}
