// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package triggerowner

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// TxRunner opens the transaction an owner-invariant write runs in. *orm.Client
// satisfies it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// Locks are the org and project locks a write takes inside its transaction,
// in the order store/org_lock.go requires: the owner's org, the project, then
// the trigger's row (Triggers.LockTrigger).
type Locks interface {
	LockUserOrgs(ctx context.Context, userID string, reads ...store.OrgRead) error
	LockProjectsForWrite(ctx context.Context, ids ...string) error
}

// Triggers locks and reads the trigger row the decision was made on.
type Triggers interface {
	LockTrigger(ctx context.Context, id string) error
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

// Adoptions re-owns the locked trigger and records the change.
type Adoptions interface {
	AdoptTrigger(ctx context.Context, t *core.Trigger) error
	RecordTriggerChange(ctx context.Context, operation string, before, after *core.Trigger) error
}

// Offers keeps the takeover offers of a trigger.
type Offers interface {
	InsertTriggerOffer(ctx context.Context, o *core.TriggerOffer) error
	GetTriggerOffer(ctx context.Context, id string) (*core.TriggerOffer, error)
	ListPendingTriggerOffers(ctx context.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error)
	SettleTriggerOffer(ctx context.Context, id, status, reason string, at time.Time) (bool, error)
	RevokeOtherTriggerOffers(ctx context.Context, triggerID, keep string, at time.Time) error
}

// OrgReads are the org-wide reads an admin lists orphans from.
type OrgReads interface {
	ListOrgTriggers(ctx context.Context, orgID string) ([]*core.Trigger, error)
	ListOrgWorkflowDrafts(ctx context.Context, orgID string) ([]*core.WorkflowDraft, error)
}
