// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// CREATE WORKFLOW TOOL
// =============================================================================

type CreateWorkflowParams struct {
	Name     *string `json:"name,omitempty" jsonschema:"description=Workflow name. If omitted a random name is generated."`
	Content  *string `json:"content,omitempty" jsonschema:"description=Initial workflow YAML content. If omitted the default agent template is used."`
	Complete *bool   `json:"complete,omitempty" jsonschema:"description=Mark the workflow complete (runnable). Rejected if it has validation errors. Default false: saved as a draft."`
}

type CreateWorkflowResult struct {
	ID     string `json:"id"`     // UUID of the created draft
	Name   string `json:"name"`   // Display name
	Slug   string `json:"slug"`   // Reference name for ref: field
	Status string `json:"status"` // "draft" or "complete"
}

type createWorkflowTool struct {
	repo db.Repository
}

const (
	CreateWorkflowToolName        = "create_workflow"
	createWorkflowToolDescription = `Create a new workflow.

Returns the workflow UUID which you can then use with get_workflow, edit_workflow, and write_workflow.

Workflows start as DRAFTS: stored even with validation errors, so you can
iterate, but never runnable. Pass complete: true (here, or on a later
edit_workflow/write_workflow) once it validates to make it runnable — that is
rejected while it has errors. YAML that does not parse is rejected either way.

**Parameters:**
- name: (optional) Workflow name. A random name is generated if omitted.
- content: (optional) Complete workflow YAML. The default agent template is used if omitted.
- complete: (optional) true to mark it complete (runnable). Default: draft.

**Response:**
The resulting status and every current validation error and warning, plus
JSON with id, name, slug, and status.

**Example — create with defaults:**
{}

**Example — create with name and content:**
{
  "name": "my-review-workflow",
  "content": "name: my-review-workflow\nentry: [agent]\nnodes:\n  - id: agent\n    type: call_llm"
}`
)

func NewCreateWorkflowTool(repo db.Repository) Tool {
	tool := &createWorkflowTool{repo: repo}
	return NewToolWrapper[CreateWorkflowParams, ToolResponse](tool)
}

func (t *createWorkflowTool) Name() string {
	return CreateWorkflowToolName
}

func (t *createWorkflowTool) Description() string {
	return createWorkflowToolDescription
}

func (t *createWorkflowTool) RequiresPermission(args CreateWorkflowParams) (bool, error) {
	return false, nil
}

func (t *createWorkflowTool) Execute(ctx *rctx.ToolContext, args CreateWorkflowParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return NewTextErrorResponse("Unable to determine user identity"), nil
	}

	now := time.Now().UTC()
	draftID := uuid.New().String()

	// Determine name: param > YAML > random
	var workflowName string
	if args.Name != nil && *args.Name != "" {
		workflowName = *args.Name
	}

	// Determine content: param > default template
	var definition string
	if args.Content != nil && *args.Content != "" {
		definition = *args.Content

		// Extract name from YAML if not provided via param
		if workflowName == "" {
			var wfMeta struct {
				Name string `yaml:"name"`
			}
			if err := yaml.Unmarshal([]byte(definition), &wfMeta); err == nil && wfMeta.Name != "" {
				workflowName = wfMeta.Name
			}
		}
	} else {
		// Use default agent template
		data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
		if err != nil {
			definition = "name: agent\ndescription: \"\"\nnodes: []"
		} else {
			definition = string(data)
		}
	}

	// Fall back to random name if still empty
	if workflowName == "" {
		workflowName = "workflow-" + uuid.New().String()[:8]
	}

	// Replace name in template if using default template and name was provided
	if args.Content == nil || *args.Content == "" {
		definition = strings.Replace(definition, "name: agent", "name: "+workflowName, 1)
	}

	slug := generateSlugFromName(workflowName)

	// Validate exactly as run start will. A draft stores despite errors; a
	// complete workflow must pass.
	status := resolveToolStatus(args.Complete, nil)
	check := validateWorkflowForTool(ctx, t.repo, definition)
	if rejection := check.gate("created", status); rejection != "" {
		return NewTextErrorResponse(rejection), nil
	}

	draft := &db.WorkflowDraft{
		ID:         draftID,
		UserID:     userID,
		Name:       workflowName,
		Slug:       slug,
		Definition: definition,
		Status:     status,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := t.repo.CreateWorkflowDraft(ctx, draft); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create workflow draft: %v", err)), nil
	}

	result := CreateWorkflowResult{
		ID:     draftID,
		Name:   workflowName,
		Slug:   slug,
		Status: string(status),
	}

	responseText := check.outcome(fmt.Sprintf(
		"Workflow '%s' created successfully.\n\nID: %s\nSlug: %s\n\nUse `get_workflow` to view the full definition, or `edit_workflow`/`write_workflow` to modify it.",
		workflowName, draftID, slug,
	), status)

	return WithResponseMetadata(NewTextResponse(responseText), result), nil
}

// =============================================================================
// EDIT WORKFLOW TOOL
// =============================================================================

type EditWorkflowParams struct {
	ID              string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
	OldString       string `json:"old_string" jsonschema:"required,description=The exact text to find and replace in the workflow YAML"`
	NewString       string `json:"new_string" jsonschema:"required,description=The replacement text"`
	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=Optional version number from get_workflow for conflict detection"`
	Complete        *bool  `json:"complete,omitempty" jsonschema:"description=true: mark complete (runnable; rejected on validation errors). false: save as a draft. Omitted: keep the current status."`
}

type editWorkflowTool struct {
	repo db.Repository
}

const (
	EditWorkflowToolName        = "edit_workflow"
	editWorkflowToolDescription = `Make precise text replacements in the workflow YAML.

Use this for small changes like:
- Adding or modifying a node
- Updating an edge condition
- Changing input parameters

The old_string must match exactly (including whitespace and indentation).
Include enough context to ensure a unique match.

**Conflict Detection:**
If you provide expected_version (from get_workflow), the edit will fail if the 
workflow was modified since you last viewed it.

**Draft vs complete:**
A draft stores the edit even with validation errors. A complete (runnable)
workflow must stay valid: an edit that introduces errors is rejected unless you
pass complete: false, which saves it as a draft. Pass complete: true to mark it
complete once it validates. The response always shows the resulting status and
every current error and warning.

**Parameters:**
- id: (optional) Workflow UUID, slug, or name. Omit it to edit the workflow this chat is editing.
- old_string: (required) Exact text to replace.
- new_string: (required) Replacement text.
- expected_version: (optional) Version number for conflict detection.
- complete: (optional) true = mark complete, false = save as draft, omitted = keep current status.

**Example:**
{
  "old_string": "  - id: agent\n    type: call_llm",
  "new_string": "  - id: agent\n    type: call_llm\n    model: \"{{inputs.model}}\""
}`
)

func NewEditWorkflowTool(repo db.Repository) Tool {
	tool := &editWorkflowTool{repo: repo}
	return NewToolWrapper[EditWorkflowParams, ToolResponse](tool)
}

func (t *editWorkflowTool) Name() string {
	return EditWorkflowToolName
}

func (t *editWorkflowTool) Description() string {
	return editWorkflowToolDescription
}

func (t *editWorkflowTool) RequiresPermission(args EditWorkflowParams) (bool, error) {
	return false, nil // Workflow edits don't require file permission
}

func (t *editWorkflowTool) Execute(ctx *rctx.ToolContext, args EditWorkflowParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.OldString == "" {
		return NewTextErrorResponse("old_string is required"), nil
	}
	if args.NewString == "" {
		return NewTextErrorResponse("new_string is required (use delete operations explicitly if removing content)"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	// Check for conflicts if expected_version is provided
	if args.ExpectedVersion != nil {
		if draft.Version != *args.ExpectedVersion {
			return NewTextErrorResponse(fmt.Sprintf(
				"Workflow was modified since you last viewed it.\n\n"+
					"Your version: %d\n"+
					"Current version: %d\n\n"+
					"Please call get_workflow again to see the latest changes.",
				*args.ExpectedVersion,
				draft.Version,
			)), nil
		}
	}

	// Apply the text replacement
	oldContent := draft.Definition
	if !strings.Contains(oldContent, args.OldString) {
		return NewTextErrorResponse(
			"old_string not found in workflow. Make sure it matches exactly, including whitespace and indentation.\n\n" +
				"Tip: Use get_workflow to see the current content.",
		), nil
	}

	// Check for multiple matches
	count := strings.Count(oldContent, args.OldString)
	if count > 1 {
		return NewTextErrorResponse(fmt.Sprintf(
			"old_string appears %d times in the workflow. Please provide more context to ensure a unique match.",
			count,
		)), nil
	}

	newContent := strings.Replace(oldContent, args.OldString, args.NewString, 1)

	// Validate exactly as run start will. A draft stores despite errors; a
	// workflow that is (or is being made) complete must pass, and on
	// rejection the stored workflow is left as it was.
	status := resolveToolStatus(args.Complete, draft)
	check := validateWorkflowForTool(ctx, t.repo, newContent)
	if rejection := check.gate("updated", status); rejection != "" {
		return NewTextErrorResponse(rejection), nil
	}

	// Extract name from the updated YAML to keep draft name in sync. The
	// gate above already rejected content that is not YAML.
	var wfMeta struct {
		Name string `yaml:"name"`
	}
	_ = yaml.Unmarshal([]byte(newContent), &wfMeta)
	workflowName := wfMeta.Name
	if workflowName == "" {
		workflowName = draft.Name // Keep existing name if not found in YAML
	}
	slug := generateSlugFromName(workflowName)

	// Save the updated draft with synced name
	if err := t.repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, workflowName, slug, newContent, status); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to save workflow: %v", err)), nil
	}

	return NewTextResponse(check.outcome("Workflow updated successfully.\n\nUse `get_workflow` to see the full result.", status)), nil
}

// =============================================================================
// WRITE WORKFLOW TOOL
// =============================================================================

type WriteWorkflowParams struct {
	// ID selects an EXISTING draft; write_workflow never creates one. Omitted,
	// it resolves to the draft this chat is editing.
	ID string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`

	// Name is optional - overrides name in YAML if provided
	Name *string `json:"name,omitempty" jsonschema:"description=Workflow name. Overrides name in YAML if provided."`

	Content string `json:"content" jsonschema:"required,description=The complete workflow YAML content"`

	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=Optional version number for conflict detection."`

	Complete *bool `json:"complete,omitempty" jsonschema:"description=true: mark complete (runnable; rejected on validation errors). false: save as a draft. Omitted: keep the current status."`
}

// WriteWorkflowResult is the structured response from write_workflow
type WriteWorkflowResult struct {
	ID      string `json:"id"`      // UUID of the draft
	Name    string `json:"name"`    // Display name
	Slug    string `json:"slug"`    // Reference name for ref: field
	Created bool   `json:"created"` // true if new, false if updated
	Status  string `json:"status"`  // "draft" or "complete"
}

type writeWorkflowTool struct {
	repo db.Repository
}

const (
	WriteWorkflowToolName        = "write_workflow"
	writeWorkflowToolDescription = `Replace an existing workflow draft with YAML content.

**Usage:**
{
  "content": "name: my-workflow\nentry: [agent]\nnodes:\n  - id: agent\n    type: call_llm"
}

The content must be valid workflow YAML with at minimum:
- name: Workflow name
- entry: List of entry point node IDs  
- nodes: Array of node definitions
- edges: Array of edge definitions (optional for single-node workflows)

**Draft vs complete:**
A draft stores the content even with validation errors. A complete (runnable)
workflow must stay valid: content with errors is rejected unless you pass
complete: false, which saves it as a draft. Pass complete: true to mark it
complete once it validates.

**Parameters:**
- id: (optional) Workflow UUID, slug, or name. Omit it to write the workflow this chat is editing.
- name: (optional) Overrides the name in YAML. Used for display name.
- content: (required) Complete workflow YAML content.
- expected_version: (optional) Version number for conflict detection.
- complete: (optional) true = mark complete, false = save as draft, omitted = keep current status.

**Response:**
The resulting status and every current validation error and warning, plus
JSON with id, name, slug, status, and created (false for updates).
The slug can be used in ref: fields to reference this workflow.`
)

func NewWriteWorkflowTool(repo db.Repository) Tool {
	tool := &writeWorkflowTool{repo: repo}
	return NewToolWrapper[WriteWorkflowParams, ToolResponse](tool)
}

func (t *writeWorkflowTool) Name() string {
	return WriteWorkflowToolName
}

func (t *writeWorkflowTool) Description() string {
	return writeWorkflowToolDescription
}

func (t *writeWorkflowTool) RequiresPermission(args WriteWorkflowParams) (bool, error) {
	return false, nil // Workflow edits don't require file permission
}

func (t *writeWorkflowTool) Execute(ctx *rctx.ToolContext, args WriteWorkflowParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.Content == "" {
		return NewTextErrorResponse("content is required"), nil
	}

	// Extract name and description from the YAML
	var wfMeta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(args.Content), &wfMeta); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to parse workflow YAML: %v", err)), nil
	}

	// Determine the workflow name: param > YAML
	workflowName := wfMeta.Name
	if args.Name != nil && *args.Name != "" {
		workflowName = *args.Name
	}
	if workflowName == "" {
		return NewTextErrorResponse("Workflow name is required. Provide it via the 'name' parameter or in the YAML content."), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	// Validate exactly as run start will. A draft stores despite errors; a
	// workflow that is (or is being made) complete must pass.
	status := resolveToolStatus(args.Complete, draft)
	check := validateWorkflowForTool(ctx, t.repo, args.Content)
	if rejection := check.gate("saved", status); rejection != "" {
		return NewTextErrorResponse(rejection), nil
	}

	// Check for conflicts if expected_version is provided
	if args.ExpectedVersion != nil {
		if draft.Version != *args.ExpectedVersion {
			return NewTextErrorResponse(fmt.Sprintf(
				"Workflow was modified since you last viewed it.\n\n"+
					"Your version: %d\n"+
					"Current version: %d\n\n"+
					"Please call get_workflow again to see the latest changes.",
				*args.ExpectedVersion,
				draft.Version,
			)), nil
		}
	}

	// Update the draft name and slug to match
	draft.Name = workflowName
	draft.Slug = generateSlugFromName(workflowName)
	if wfMeta.Description != "" {
		draft.Description = &wfMeta.Description
	}

	// Save the updated draft with synced name/slug
	if err := t.repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, draft.Name, draft.Slug, args.Content, status); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to save workflow: %v", err)), nil
	}
	var created bool // always false - we only update

	// Build the result
	result := WriteWorkflowResult{
		ID:      draft.ID,
		Name:    draft.Name,
		Slug:    draft.Slug,
		Created: created,
		Status:  string(status),
	}

	// Format response with both text and structured data
	action := "updated"
	if created {
		action = "created"
	}

	responseText := check.outcome(fmt.Sprintf(
		"Workflow '%s' %s successfully.\n\nID: %s\nSlug: %s (use in ref: fields)\n\nUse `get_workflow` to see the full result.",
		draft.Name, action, draft.ID, draft.Slug,
	), status)

	return WithResponseMetadata(NewTextResponse(responseText), result), nil
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// generateSlugFromName creates a URL-safe slug from a workflow name
func generateSlugFromName(name string) string {
	// Convert to lowercase
	slug := strings.ToLower(name)
	// Replace spaces and underscores with hyphens
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	// Remove any characters that aren't alphanumeric or hyphens
	var result strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	slug = result.String()
	// Remove consecutive hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")
	return slug
}
