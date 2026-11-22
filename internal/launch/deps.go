// forge:deps — consumer-declared interfaces for this package's Deps.
// Copyright (c) 2025 Reliant Labs

package launch

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
)

// TaskQueue names the Temporal task queue runs execute on. A workflow-engine
// constant the caller passes (workflow.SharedTaskQueue), not deployment
// configuration, so it is a named type rather than a bare string.
type TaskQueue string

// Deps is what a Launcher is built from. Plain Deps, not a forge contract
// package: it is built by hand by the worker provider and the chat handler.
type Deps struct {
	// Store is everything a launch reads and writes in the database.
	Store Store
	// Selections is the chat selection store: the layer between a chat's
	// presets and a send's params, and where a start records its first choice.
	// store.ChatSelectionExtStore satisfies it.
	Selections chatselection.Store
	// Threads creates the root workflow and its thread inside the launch
	// transaction.
	Threads ThreadCreator
	// Temporal starts the run.
	Temporal TemporalStarter
	// Runs writes the run ids back onto the chat once Temporal accepted the
	// start.
	Runs RunRecorder
	// TaskQueue is the queue runs execute on: workflow.SharedTaskQueue.
	TaskQueue TaskQueue
	// Pinner pins an unpinned chat to a machine before its run starts. Nil
	// pins nothing: the chat runs wherever default resolution points on each
	// call.
	Pinner ChatPinner
}

// ChatWriter creates and updates the chat a launch materializes.
type ChatWriter interface {
	// LockUserOrgs takes the owner's org lock for the launch's transaction,
	// before any row lock, and re-checks the orgs a new chat's org was
	// decided from (db.ErrOrgChanged when a backfill changed one).
	LockUserOrgs(ctx context.Context, userID string, reads ...db.OrgRead) error
	CreateChat(ctx context.Context, chat *db.Chat) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	UpdateChat(ctx context.Context, chat *db.Chat) error
	UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error
	CountMessagesInChat(ctx context.Context, chatID string) (int, error)
	SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*db.Message, error)
	SaveMessageToThreadOnce(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (msg *db.Message, created bool, err error)
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
	ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any, def *core.TriggerEventDefinition) (claimed bool, err error)
}

// WorkflowStore is the root workflow row a launch starts.
type WorkflowStore interface {
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	UpdateWorkflowName(ctx context.Context, id string, workflowName string) error
	// MarkWorkflowStartFailed records a failed Temporal start on a PENDING
	// root; ClaimWorkflowStart clears it before a retry, and false means the
	// root left PENDING, so the start is no longer the caller's to make.
	MarkWorkflowStartFailed(ctx context.Context, id string) (bool, error)
	ClaimWorkflowStart(ctx context.Context, id string) (bool, error)
}

// PinnedWorkflowResolver is a WorkflowResolver that also serves a pinned
// catalog scope (workflowsource.PinnedStore), which resolution asks for by
// assertion when a scope carries a catalog pin. The per-request resolver
// (requestResolver) is one, memoizing pins across one ListTriggers (#2008).
type PinnedWorkflowResolver interface {
	WorkflowResolver
	GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error)
}

// WorkflowProvenanceReader and MachineNamer are the reads a trigger's
// definition is attributed with (triggers.describeDefinition asks its store
// for them by assertion): which machine synced the file, and that machine's
// name. The per-request resolver (requestResolver) serves both, memoized,
// when the store it wraps does (#2283).
type WorkflowProvenanceReader interface {
	ProjectWorkflowProvenance(ctx context.Context, projectID string) ([]config.WorkflowProvenance, error)
}

// MachineNamer names a machine for display: see WorkflowProvenanceReader.
type MachineNamer interface {
	MachineDisplayName(ctx context.Context, daemonID string) string
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
	// One machine's own synced catalog (workflowsource.MachineStore), which a
	// trigger's run on that machine resolves against; synced is false when
	// the machine has never synced the project.
	GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (workflows *string, synced bool, err error)
	GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (presets *string, synced bool, err error)
	// WorkflowSourceDiagnostic (workflowsource.SourceDiagnostics) says, on a
	// miss, which machine provides the workflow and at which path.
	WorkflowSourceDiagnostic(ctx context.Context, projectID, daemonID, ref string) (string, error)
}

// ProjectLookup resolves the project, worktrees and settings a launch runs in.
//
// Installs is each machine's checkout of a project (project_daemons): a
// chat's main checkout is its machine's, never projects.path.
type ProjectLookup interface {
	daemonplacement.Installs
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
}

// Transactor runs f in one transaction; repository calls made with the ctx f
// receives join it.
type Transactor interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
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
