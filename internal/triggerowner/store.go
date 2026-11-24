package triggerowner

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Stores is the production implementation of Locks, Triggers, Offers and
// OrgReads over one orm handle. The queries it forwards to live in
// internal/store functions that are not in an entity's *_repo_ext.go (the
// takeover queries span the no-entity trigger_offers table), so forge
// generates no ExtStore for them; this is the one hand adapter, one line per
// query. Called with the ctx RunTx hands its fn, each query joins that
// transaction.
type Stores struct{ db orm.Context }

// NewStores returns the stores over db: the *orm.Client for ordinary use.
func NewStores(db orm.Context) Stores { return Stores{db: db} }

func (s Stores) LockUserOrgs(ctx context.Context, userID string, reads ...store.OrgRead) error {
	return store.LockUserOrgs(ctx, s.db, userID, reads...)
}

func (s Stores) LockProjectsForWrite(ctx context.Context, ids ...string) error {
	return store.LockProjectsForWrite(ctx, s.db, ids...)
}

func (s Stores) LockTrigger(ctx context.Context, id string) error {
	return store.LockTrigger(ctx, s.db, id)
}

func (s Stores) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	return store.GetTrigger(ctx, s.db, id)
}

func (s Stores) AdoptTrigger(ctx context.Context, t *core.Trigger) error {
	return store.AdoptTrigger(ctx, s.db, t)
}

func (s Stores) RecordTriggerChange(ctx context.Context, operation string, before, after *core.Trigger) error {
	return store.RecordTriggerChange(ctx, s.db, operation, before, after)
}

func (s Stores) InsertTriggerOffer(ctx context.Context, o *core.TriggerOffer) error {
	return store.InsertTriggerOffer(ctx, s.db, o)
}

func (s Stores) GetTriggerOffer(ctx context.Context, id string) (*core.TriggerOffer, error) {
	return store.GetTriggerOffer(ctx, s.db, id)
}

func (s Stores) ListPendingTriggerOffers(ctx context.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error) {
	return store.ListPendingTriggerOffers(ctx, s.db, f)
}

func (s Stores) SettleTriggerOffer(ctx context.Context, id, status, reason string, at time.Time) (bool, error) {
	return store.SettleTriggerOffer(ctx, s.db, id, status, reason, at)
}

func (s Stores) RevokeOtherTriggerOffers(ctx context.Context, triggerID, keep string, at time.Time) error {
	return store.RevokeOtherTriggerOffers(ctx, s.db, triggerID, keep, at)
}

func (s Stores) ListOrgTriggers(ctx context.Context, orgID string) ([]*core.Trigger, error) {
	return store.ListOrgTriggers(ctx, s.db, orgID)
}

func (s Stores) ListOrgWorkflowDrafts(ctx context.Context, orgID string) ([]*core.WorkflowDraft, error) {
	return store.ListOrgWorkflowDrafts(ctx, s.db, orgID)
}
