package core

import (
	"database/sql"
	"fmt"
	"time"
)

// WorkflowDraft represents a user-owned workflow definition stored in the database.
// Workflows are available across all projects. Project-specific workflows come from
// .reliant/workflows/*.yaml files (read-only, not stored in DB).
// A workflow is "usable" (shows in agent selector, can be loaded at runtime)
// when Status is WorkflowDraftStatusComplete and IsHidden=false. Validity is never
// stored — it is computed on read and re-checked at run start.
type WorkflowDraft struct {
	ID          string              `json:"id"`
	UserID      string              `json:"user_id"`
	Name        string              `json:"name"` // Display name (can have spaces, caps)
	Slug        string              `json:"slug"` // Runtime reference name (lowercase, hyphenated)
	Description *string             `json:"description,omitempty"`
	Definition  string              `json:"definition"`            // YAML workflow definition
	Status      WorkflowDraftStatus `json:"status"`                // draft (work in progress) or complete (runnable)
	SourcePath  *string             `json:"source_path,omitempty"` // Original file path if imported
	ForkedFrom  *string             `json:"forked_from,omitempty"` // Origin workflow (e.g., "builtin://agent")
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	IsHidden    bool                `json:"is_hidden"`
	Version     int64               `json:"version"` // OCC version number
}

// WorkflowDraftStatus is a stored workflow's lifecycle state.
type WorkflowDraftStatus string

const (
	// WorkflowDraftStatusDraft is work in progress: saved as-is, possibly invalid,
	// never runnable and never offered where a runnable workflow is required.
	WorkflowDraftStatusDraft WorkflowDraftStatus = "draft"
	// WorkflowDraftStatusComplete passed validation when it was marked complete and
	// is runnable. Every later save must keep it valid.
	WorkflowDraftStatusComplete WorkflowDraftStatus = "complete"
)

// Valid reports whether s is a known status.
func (s WorkflowDraftStatus) Valid() bool {
	return s == WorkflowDraftStatusDraft || s == WorkflowDraftStatusComplete
}

// WorkflowDraftNotRunnableError is returned by GetUsableWorkflowBySlug when
// the slug names a visible workflow that is still a draft. Runtime paths
// (run start, `ref:`, spawn, routers, scenarios) surface it instead of a bare
// "not found", so the user learns the one action that fixes it.
type WorkflowDraftNotRunnableError struct {
	Slug string
}

func (e *WorkflowDraftNotRunnableError) Error() string {
	return fmt.Sprintf("workflow %q is a draft and cannot run — mark it complete first", e.Slug)
}

// WorkflowScenario represents a test scenario for a workflow.
type WorkflowScenario struct {
	ID              string         `json:"id"`
	WorkflowDraftID sql.NullString `json:"workflow_draft_id,omitempty"` // FK to workflow_drafts (NULL if testing raw YAML)
	UserID          string         `json:"user_id"`
	Name            string         `json:"name"`
	Description     sql.NullString `json:"description,omitempty"`
	Events          string         `json:"events"`           // JSON array of event objects
	Expect          sql.NullString `json:"expect,omitempty"` // JSON object with expected outcome
	LastRunAt       sql.NullTime   `json:"last_run_at,omitempty"`
	LastRunStatus   sql.NullString `json:"last_run_status,omitempty"` // passed, failed, error
	LastRunResult   sql.NullString `json:"last_run_result,omitempty"` // JSON object with execution details
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Version         int64          `json:"version"` // OCC version number
}

// Preset represents a user-created preset stored in the database.
// Presets bundle workflow parameter values that can be applied to workflows.
// They support both global (user-wide) and project-specific scopes.
type Preset struct {
	ID          string                 `json:"id"`
	UserID      string                 `json:"user_id"`
	ProjectID   *string                `json:"project_id,omitempty"` // NULL = global (user-wide)
	Name        string                 `json:"name"`                 // Display name
	Slug        string                 `json:"slug"`                 // URL-safe identifier
	Description *string                `json:"description,omitempty"`
	Tag         string                 `json:"tag"`    // Target workflow/group tag
	Params      map[string]interface{} `json:"params"` // Parameter values
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}
