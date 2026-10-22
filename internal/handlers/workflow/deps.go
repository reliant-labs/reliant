// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package workflow

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AutomationRetirer is the trigger service's cleanup after automations are
// deleted from the database. Declared here, at the consumer.
type AutomationRetirer interface {
	RetireDeletedTriggers(ctx context.Context, deleted []*core.Trigger)
}

// Repository is the slice of db.Repository the workflow handlers use. The
// last three are what workflowcatalog, workflowsource and launch read
// through it when they resolve a workflow's refs.
type Repository interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)

	GetWorkflowDraft(ctx context.Context, id string) (*db.WorkflowDraft, error)
	GetWorkflowDraftByName(ctx context.Context, userID, name string) (*db.WorkflowDraft, error)
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	CreateWorkflowDraft(ctx context.Context, draft *db.WorkflowDraft) error
	UpdateWorkflowDraft(ctx context.Context, draft *db.WorkflowDraft) error
	UpsertWorkflowDraft(ctx context.Context, draft *db.WorkflowDraft) (*db.WorkflowDraft, error)
	UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*db.WorkflowDraft, error)
	SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*db.WorkflowDraft, error)
	SetWorkflowDraftStatus(ctx context.Context, id string, status db.WorkflowDraftStatus) (*db.WorkflowDraft, error)
	DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error

	// What deleting a workflow does to the automations that run it.
	ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
	DeleteTrigger(ctx context.Context, id string) error
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error

	ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error)
	ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error)

	ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*db.WorkflowDraft, error)
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error)
}

// Sender sends one request/response command to a user's daemon.
// toolexec.DaemonRouter implements it.
type Sender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}
