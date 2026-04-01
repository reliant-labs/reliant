package core

import (
	"context"
	"database/sql"
	"time"
)

// WorkflowDraft represents a user-owned workflow definition stored in the database.
// Workflows are available across all projects. Project-specific workflows come from
// .reliant/workflows/*.yaml files (read-only, not stored in DB).
// A workflow is "usable" (shows in agent selector, can be loaded at runtime)
// when IsValid=true and IsHidden=false.
type WorkflowDraft struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	Name             string    `json:"name"` // Display name (can have spaces, caps)
	Slug             string    `json:"slug"` // Runtime reference name (lowercase, hyphenated)
	Description      *string   `json:"description,omitempty"`
	Definition       string    `json:"definition"`            // YAML workflow definition
	IsValid          bool      `json:"is_valid"`              // Passes validation
	ValidationErrors *string   `json:"validation_errors"`     // JSON array of errors
	SourcePath       *string   `json:"source_path,omitempty"` // Original file path if imported
	ForkedFrom       *string   `json:"forked_from,omitempty"` // Origin workflow (e.g., "builtin://agent")
	ChatID           *string   `json:"chat_id,omitempty"`     // Associated chat for implicit lookup
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	IsHidden         bool      `json:"is_hidden"`
	Version          int64     `json:"version"` // OCC version number
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

// WorkflowCatalogStore is the shared contract for workflow draft/scenario/preset persistence across drivers.
type WorkflowCatalogStore interface {
	CreateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error
	UpsertWorkflowDraft(ctx context.Context, draft *WorkflowDraft) (*WorkflowDraft, error)
	GetWorkflowDraft(ctx context.Context, id string) (*WorkflowDraft, error)
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error)
	GetWorkflowDraftByName(ctx context.Context, userID, name string) (*WorkflowDraft, error)
	GetWorkflowDraftByChatID(ctx context.Context, chatID string) (*WorkflowDraft, error)
	GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*WorkflowDraft, error)
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error)
	ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error)
	ListUsableWorkflowsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error)
	UpdateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error
	UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, isValid bool, validationErrors *string) error
	SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*WorkflowDraft, error)
	DeleteWorkflowDraft(ctx context.Context, id string) error
	DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error
	WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error)
	CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error)
	AssociateChatWithDraft(ctx context.Context, draftID string, chatID string) (*WorkflowDraft, error)
	UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*WorkflowDraft, error)

	CreatePreset(ctx context.Context, preset *Preset) (*Preset, error)
	UpsertPreset(ctx context.Context, preset *Preset) (*Preset, error)
	GetPreset(ctx context.Context, id string) (*Preset, error)
	GetPresetBySlug(ctx context.Context, userID, slug string) (*Preset, error)
	GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*Preset, error)
	ListUserPresets(ctx context.Context, userID string) ([]*Preset, error)
	ListUserPresetsGlobal(ctx context.Context, userID string) ([]*Preset, error)
	ListUserPresetsByProject(ctx context.Context, userID, projectID string) ([]*Preset, error)
	ListPresetsByTag(ctx context.Context, userID, tag, projectID string) ([]*Preset, error)
	UpdatePreset(ctx context.Context, preset *Preset) (*Preset, error)
	DeletePreset(ctx context.Context, id string) error
	DeletePresetBySlug(ctx context.Context, userID, slug string) error
	DeletePresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) error

	CreateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error
	GetWorkflowScenario(ctx context.Context, id string) (*WorkflowScenario, error)
	GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*WorkflowScenario, error)
	ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*WorkflowScenario, error)
	ListWorkflowScenariosByUser(ctx context.Context, userID string) ([]*WorkflowScenario, error)
	UpdateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error
	UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error
	DeleteWorkflowScenario(ctx context.Context, id string) error
	DeleteWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) error
}
