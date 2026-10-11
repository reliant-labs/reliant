// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// workflowCheck is the outcome of validating a workflow definition the way
// the runtime will when it is run: the same static analysis, with the user's
// workflow loader so `ref:` outputs type-check against the real child.
type workflowCheck struct {
	// parseErr is set when the definition could not be parsed at all.
	parseErr error
	result   *validation.Result
	// workflow is the parsed definition, used to place findings on the node
	// they are about (an edge finding names the edge by index).
	workflow *reliantv1.Workflow
}

func (c workflowCheck) valid() bool {
	return c.parseErr == nil && (c.result == nil || !c.result.HasErrors())
}

// protoErrors renders every error (and, with warnings=true, every warning) as
// structured ValidationErrors. Warnings carry type "warning:<category>" so a
// client can tell them apart without a proto change.
func (c workflowCheck) protoErrors(warnings bool) []*reliantv1.ValidationError {
	if c.parseErr != nil {
		return []*reliantv1.ValidationError{{Type: "conversion_error", Message: c.parseErr.Error(), Detail: c.parseErr.Error()}}
	}
	if c.result == nil {
		return nil
	}
	var out []*reliantv1.ValidationError
	for _, e := range c.result.Errors() {
		out = append(out, c.protoFinding(string(e.Category), e))
	}
	if warnings {
		for _, w := range c.result.Warnings() {
			out = append(out, c.protoFinding("warning:"+string(w.Category), w))
		}
	}
	return out
}

func (c workflowCheck) protoFinding(findingType string, e *validation.Error) *reliantv1.ValidationError {
	nodeID, field := locateFinding(e.Path, e.Field, c.workflow)
	location := append(append([]string{}, e.Path...), nonEmpty(e.Field)...)
	return &reliantv1.ValidationError{
		Type:       findingType,
		Message:    e.Error(),
		Suggestion: e.Suggestion,
		NodeId:     nodeID,
		Field:      field,
		Detail:     e.Message,
		Path:       strings.Join(location, "."),
	}
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// nodePathSegment is how the validator names a node in a path: "[2](call_llm)".
var nodePathSegment = regexp.MustCompile(`^\[(\d+)\]\((.*)\)$`)

// edgePathSegment is how the validator names an edge in a path: "[3]".
var edgePathSegment = regexp.MustCompile(`^\[(\d+)\]$`)

// locateFinding places a validation finding on the top-level node it is
// about, and the field within that node. Paths look like
//
//	[<workflow>, nodes, [1](call_llm), system_prompt]                 field "system_prompt"
//	[<workflow>, nodes, [0](loop), inline, nodes, [2](x)] + "model"   field "inline.nodes.[2](x).model"
//	[<workflow>, edges, [3], cases, [0], condition]                   the edge's source node
//
// with the validator's own Field appended. Workflow-level findings (entry,
// inputs, triggers) have no node.
func locateFinding(path []string, field string, wf *reliantv1.Workflow) (nodeID, nodeField string) {
	rest := append(append([]string{}, path...), nonEmpty(field)...)
	if len(rest) > 0 {
		rest = rest[1:] // the workflow's own name
	}
	if len(rest) < 2 {
		return "", ""
	}
	switch rest[0] {
	case "nodes":
		if m := nodePathSegment.FindStringSubmatch(rest[1]); m != nil {
			return m[2], strings.Join(rest[2:], ".")
		}
		// Some checks name the node bare: [<workflow>, nodes, summarize].
		if !strings.HasPrefix(rest[1], "[") {
			return rest[1], strings.Join(rest[2:], ".")
		}
	case "edges":
		m := edgePathSegment.FindStringSubmatch(rest[1])
		if m == nil || wf == nil {
			return "", ""
		}
		index, err := strconv.Atoi(m[1])
		if err != nil || index >= len(wf.GetEdges()) {
			return "", ""
		}
		return edgeSourceNode(wf.GetEdges()[index].GetFrom()), ""
	}
	return "", ""
}

// edgeSourceNode is the node an edge leaves: `from` names a node, or an
// event on one ("build.failed").
func edgeSourceNode(from string) string {
	node, _, _ := strings.Cut(from, ".")
	return node
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
//
// Refs resolve the way run start does (workflowsource.DraftLoader): only a
// complete workflow loads, and a draft child is an error (a parent that refs
// a draft would fail at run start). The workflow being validated resolves to
// itself, so a workflow that spawns itself can be validated — and marked
// complete — while it is still a draft.
func (s *WorkflowService) validateWorkflowDefinition(ctx context.Context, userID string, definition []byte) workflowCheck {
	return validateWorkflowDefinitionWith(definition, func(self *reliantv1.Workflow) v2.WorkflowLoader {
		return workflowsource.DraftLoader(ctx, s.database, userID, self)
	})
}

// validateWorkflowDefinitionWith validates definition, resolving its refs
// with the loader loaderFor returns for the parsed definition. A listing
// passes one built over its workflowCatalog and shared by every workflow it
// validates (see ListWorkflows).
func validateWorkflowDefinitionWith(definition []byte, loaderFor func(self *reliantv1.Workflow) v2.WorkflowLoader) workflowCheck {
	self, err := wfyaml.ParseWorkflow(definition)
	if err != nil {
		return workflowCheck{parseErr: fmt.Errorf("failed to parse workflow: %w", err)}
	}
	result, err := v2.ValidateYAMLResult(definition, loaderFor(self))
	if err != nil {
		return workflowCheck{parseErr: err}
	}
	return workflowCheck{result: result, workflow: self}
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
