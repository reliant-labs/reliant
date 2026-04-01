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
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// CREATE WORKFLOW TOOL
// =============================================================================

type CreateWorkflowParams struct {
	Name    *string `json:"name,omitempty" jsonschema:"description=Workflow name. If omitted a random name is generated."`
	Content *string `json:"content,omitempty" jsonschema:"description=Initial workflow YAML content. If omitted the default agent template is used."`
}

type CreateWorkflowResult struct {
	ID   string `json:"id"`   // UUID of the created draft
	Name string `json:"name"` // Display name
	Slug string `json:"slug"` // Reference name for ref: field
}

type createWorkflowTool struct {
	repo db.Repository
}

const (
	CreateWorkflowToolName        = "create_workflow"
	createWorkflowToolDescription = `Create a new workflow draft.

Returns the draft UUID which you can then use with get_workflow, edit_workflow, and write_workflow.

**Parameters:**
- name: (optional) Workflow name. A random name is generated if omitted.
- content: (optional) Complete workflow YAML. The default agent template is used if omitted.

**Response:**
Returns JSON with id, name, and slug.

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

	// Validate the YAML (informational only — save regardless)
	validationErr := validateWorkflowYAML(definition)

	draft := &db.WorkflowDraft{
		ID:         draftID,
		UserID:     userID,
		Name:       workflowName,
		Slug:       slug,
		Definition: definition,
		IsValid:    validationErr == nil,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := t.repo.CreateWorkflowDraft(ctx, draft); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create workflow draft: %v", err)), nil
	}

	result := CreateWorkflowResult{
		ID:   draftID,
		Name: workflowName,
		Slug: slug,
	}

	var responseText string
	if validationErr != nil {
		responseText = fmt.Sprintf(
			"Workflow '%s' created with validation errors:\n\n%v\n\n"+
				"ID: %s\nSlug: %s\n\nUse `get_workflow` to view, `edit_workflow` or `write_workflow` to fix.",
			workflowName, validationErr, draftID, slug,
		)
	} else {
		responseText = fmt.Sprintf(
			"Workflow '%s' created successfully.\n\nID: %s\nSlug: %s\n\nUse `get_workflow` to view the full definition, or `edit_workflow`/`write_workflow` to modify it.",
			workflowName, draftID, slug,
		)
	}

	return WithResponseMetadata(NewTextResponse(responseText), result), nil
}

// =============================================================================
// EDIT WORKFLOW TOOL
// =============================================================================

type EditWorkflowParams struct {
	ID              string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
	OldString       string `json:"old_string" jsonschema:"required,description=The exact text to find and replace in the workflow YAML"`
	NewString       string `json:"new_string" jsonschema:"required,description=The replacement text"`
	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=Optional version number from get_workflow for conflict detection"`
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

**Example:**
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
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

	// Get draft by ID
	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
	}
	if draft == nil {
		return NewTextErrorResponse(fmt.Sprintf("Workflow draft %s not found", args.ID)), nil
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

	// Validate the new YAML (but save regardless)
	validationErr := validateWorkflowYAML(newContent)

	// Extract name from the updated YAML to keep draft name in sync
	var wfMeta struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(newContent), &wfMeta); err != nil {
		workflowName := draft.Name
		slug := generateSlugFromName(workflowName)
		if err := t.repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, workflowName, slug, newContent, true, nil); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to save workflow: %v", err)), nil
		}
		return NewTextResponse(fmt.Sprintf(
			"Workflow saved, but name extraction from YAML failed (%v). Keeping existing draft name.\n\nUse `get_workflow` to inspect and adjust if needed.",
			err,
		)), nil
	}
	workflowName := wfMeta.Name
	if workflowName == "" {
		workflowName = draft.Name // Keep existing name if not found in YAML
	}
	slug := generateSlugFromName(workflowName)

	// Save the updated draft with synced name
	if err := t.repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, workflowName, slug, newContent, true, nil); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to save workflow: %v", err)), nil
	}

	if validationErr != nil {
		return NewTextResponse(fmt.Sprintf(
			"Workflow saved with validation errors:\n\n%v\n\n"+
				"Your changes were saved. You can use `edit_workflow` to make further modifications, "+
				"or `write_workflow` to replace the entire content.",
			validationErr,
		)), nil
	}

	return NewTextResponse("Workflow updated successfully.\n\nUse `get_workflow` to see the full result."), nil
}

// =============================================================================
// WRITE WORKFLOW TOOL
// =============================================================================

type WriteWorkflowParams struct {
	// ID is required - always write to an existing draft (created upfront via createWorkflowDraft)
	ID string `json:"id" jsonschema:"required,description=Workflow draft UUID (required)"`

	// Name is optional - overrides name in YAML if provided
	Name *string `json:"name,omitempty" jsonschema:"description=Workflow name. Overrides name in YAML if provided."`

	Content string `json:"content" jsonschema:"required,description=The complete workflow YAML content"`

	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=Optional version number for conflict detection."`
}

// WriteWorkflowResult is the structured response from write_workflow
type WriteWorkflowResult struct {
	ID      string `json:"id"`      // UUID of the draft
	Name    string `json:"name"`    // Display name
	Slug    string `json:"slug"`    // Reference name for ref: field
	Created bool   `json:"created"` // true if new, false if updated
}

type writeWorkflowTool struct {
	repo db.Repository
}

const (
	WriteWorkflowToolName        = "write_workflow"
	writeWorkflowToolDescription = `Replace an existing workflow draft with YAML content.

**Usage:**
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "content": "name: my-workflow\nentry: [agent]\nnodes:\n  - id: agent\n    type: call_llm"
}

The content must be valid workflow YAML with at minimum:
- name: Workflow name
- entry: List of entry point node IDs  
- nodes: Array of node definitions
- edges: Array of edge definitions (optional for single-node workflows)

**Parameters:**
- id: (required) Workflow draft UUID.
- name: (optional) Overrides the name in YAML. Used for display name.
- content: (required) Complete workflow YAML content.
- expected_version: (optional) Version number for conflict detection.

**Response:**
Returns JSON with id, name, slug, and created (false for updates).
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

	if args.ID == "" {
		return NewTextErrorResponse("id is required"), nil
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

	// Validate the YAML (but save regardless)
	validationErr := validateWorkflowYAML(args.Content)

	// Get existing draft
	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
	}
	if draft == nil {
		return NewTextErrorResponse(fmt.Sprintf("Workflow draft %s not found", args.ID)), nil
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
	if err := t.repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, draft.Name, draft.Slug, args.Content, true, nil); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to save workflow: %v", err)), nil
	}
	var created bool // always false - we only update

	// Build the result
	result := WriteWorkflowResult{
		ID:      draft.ID,
		Name:    draft.Name,
		Slug:    draft.Slug,
		Created: created,
	}

	// Format response with both text and structured data
	action := "updated"
	if created {
		action = "created"
	}

	var responseText string
	if validationErr != nil {
		responseText = fmt.Sprintf(
			"Workflow '%s' %s with validation errors:\n\n%v\n\n"+
				"Your changes were saved. You can use `edit_workflow` to make further modifications.\n\n"+
				"ID: %s\nSlug: %s (use in ref: fields)",
			draft.Name, action, validationErr, draft.ID, draft.Slug,
		)
	} else {
		responseText = fmt.Sprintf(
			"Workflow '%s' %s successfully.\n\nID: %s\nSlug: %s (use in ref: fields)\n\nUse `get_workflow` to see the full result.",
			draft.Name, action, draft.ID, draft.Slug,
		)
	}

	return WithResponseMetadata(NewTextResponse(responseText), result), nil
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// validateWorkflowYAML parses and validates the workflow YAML
func validateWorkflowYAML(content string) error {
	// First check it's valid YAML
	var raw interface{}
	if err := yaml.Unmarshal([]byte(content), &raw); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Try to parse as a workflow using the v2 parser
	_, err := v2.ParseWorkflowProtoBytes([]byte(content))
	if err != nil {
		return fmt.Errorf("workflow validation failed: %w", err)
	}

	return nil
}

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
