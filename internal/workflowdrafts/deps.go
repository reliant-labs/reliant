// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package workflowdrafts

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// TxRunner opens the transaction a draft write runs in. *db.Repo satisfies
// it, so the retry ladder a write ran under before it left the facade is the
// one it still runs under.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// OrgLocks takes a user's org lock for the rest of the surrounding
// transaction (store.LockUserOrgs). *db.Repo satisfies it.
type OrgLocks interface {
	LockUserOrgs(ctx context.Context, userID string, reads ...store.OrgRead) error
}

// UserUpdates announces a draft write to its owner's open editors, in the
// write's own transaction. *db.Repo satisfies it.
type UserUpdates interface {
	CreateUserUpdate(ctx context.Context, update *core.UserUpdate) error
}

// Drafts is the workflow_drafts rows. store.WorkflowDraftExtStore satisfies
// it.
type Drafts interface {
	InsertWorkflowDraft(ctx context.Context, d *core.WorkflowDraft) error
	UpsertWorkflowDraftBySlug(ctx context.Context, d *core.WorkflowDraft, over string) (*core.WorkflowDraft, error)
	ReplaceWorkflowDraft(ctx context.Context, d *core.WorkflowDraft) error
	SetWorkflowDraftDefinition(ctx context.Context, id, name, slug, definition string, status core.WorkflowDraftStatus) error
	SetWorkflowDraftStatus(ctx context.Context, id string, status core.WorkflowDraftStatus) (*core.WorkflowDraft, error)
	SetWorkflowDraftHidden(ctx context.Context, id string, hidden bool) (*core.WorkflowDraft, error)
	SetWorkflowDraftForkedFrom(ctx context.Context, id, forkedFrom string) (*core.WorkflowDraft, error)
	RemoveWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error)
	LockWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error)
	LockWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error)
	WorkflowDraftOwner(ctx context.Context, id string) (string, error)
	LoadWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error)
	FindWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error)
	FindWorkflowDraftByName(ctx context.Context, userID, name string) (*core.WorkflowDraft, error)
	FindUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error)
	ListWorkflowDraftsBySlugs(ctx context.Context, userID string, slugs []string) ([]*core.WorkflowDraft, error)
	ListUserWorkflowDrafts(ctx context.Context, userID string) ([]*core.WorkflowDraft, error)
}
