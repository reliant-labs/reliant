// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package workflow

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// AutomationRetirer is the trigger service's cleanup after automations are
// deleted from the database. Declared here, at the consumer.
type AutomationRetirer interface {
	RetireDeletedTriggers(ctx context.Context, deleted []*core.Trigger)
}

// Repository is the slice of *db.Repo the workflow handlers use. The
// last three are what workflowcatalog, workflowsource and launch read
// through it when they resolve a workflow's refs.
// OrgResolver answers the org questions of these rpcs. *orgs.Resolver is
// one; nil (or a nil *orgs.Resolver) is a deployment with no orgs.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
	DefaultOrg(ctx context.Context, userID string) (string, error)
	ActingOrg(ctx context.Context, userID, requested string) (string, error)
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
}

type Repository interface {
	// AssignUnassignedOrg stamps the caller's rows that have no org with
	// their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error
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

	// Installs is each machine's checkout of the project and who owns each
	// machine (projectcheckout.Resolve).
	daemonplacement.Installs
}

// Sender sends one request/response command to the machine holding a
// project's checkout. *toolexec.NATSDaemonRouter implements it.
type Sender interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// PlacementDaemonID ranks the machines holding a checkout the way the
	// project's other work is placed (daemonplacement.Machines).
	PlacementDaemonID(ctx context.Context, userID, daemonID string) (string, error)
}
