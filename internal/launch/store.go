// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
)

// The Launcher reads and writes through these role interfaces instead of the
// whole db.Repository, so each dependency of a launch is visible here and a
// fake implements only what a test exercises.

// ChatWriter creates and updates the chat a launch materializes.
type ChatWriter interface {
	CreateChat(ctx context.Context, chat *db.Chat) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	UpdateChat(ctx context.Context, chat *db.Chat) error
	UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error
	CountMessagesInChat(ctx context.Context, chatID string) (int, error)
	SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*db.Message, error)
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
}

// TriggerEventStore is the trigger_events ledger a launch dedupes against.
type TriggerEventStore interface {
	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	UpdateTriggerEventOutcome(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error
	UpdateTriggerEventPayload(ctx context.Context, id string, payload map[string]any) error
	// ClaimPendingTriggerEvent adopts an inbound event a receiver recorded
	// as pending; claimed=false means another launch got it first.
	ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any) (claimed bool, err error)
}

// WorkflowStore is the root workflow row a launch starts.
type WorkflowStore interface {
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	UpdateWorkflowName(ctx context.Context, id string, workflowName string) error
}

// WorkflowResolver looks up the workflow definition and presets a spec names.
type WorkflowResolver interface {
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetPresetBySlug(ctx context.Context, userID, slug string) (*db.Preset, error)
	GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*db.Preset, error)
	// One column each, never the whole project config record: see
	// db.Repo.GetProjectWorkflowsJSON.
	GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
}

// ProjectLookup resolves the project, worktrees and settings a launch runs in.
type ProjectLookup interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
}

// Transactor runs f in one transaction; repository calls made with the ctx f
// receives join it.
type Transactor interface {
	RunTx(ctx context.Context, f func(ctx context.Context) error) error
}

// Store is everything a Launcher needs from the database. *db.Repo satisfies it.
type Store interface {
	ChatWriter
	TriggerEventStore
	WorkflowStore
	WorkflowResolver
	ProjectLookup
	Transactor
}

// ThreadCreator creates the root workflow and its thread inside the launch
// transaction. Satisfied by *threads.Service.
type ThreadCreator interface {
	CreateWorkflowWithThread(ctx context.Context, opts threads.CreateWorkflowWithThreadOpts) (*db.Workflow, *db.Thread, *db.ContextWindow, error)
}

var (
	_ Store         = db.Repository(nil)
	_ ThreadCreator = (*threads.Service)(nil)
)
