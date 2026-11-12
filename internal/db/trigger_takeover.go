// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The admin takeover of a departed member's automations
// (control-plane docs/design/org-sharing.md §8; store/trigger_takeover.go).

// ListOrgTriggers lists every trigger of one org, whoever owns it. Only the
// takeover's admin read calls it; the handler decides who may.
func (r *Repo) ListOrgTriggers(ctx context.Context, orgID string) ([]*core.Trigger, error) {
	if orgID == "" {
		return nil, fmt.Errorf("org ID cannot be empty")
	}
	return store.ListOrgTriggers(ctx, r.ORM(), orgID)
}

// ListOrgWorkflowDrafts lists every saved workflow of one org, whoever owns
// it. Only the takeover's admin read calls it.
func (r *Repo) ListOrgWorkflowDrafts(ctx context.Context, orgID string) ([]*core.WorkflowDraft, error) {
	if orgID == "" {
		return nil, fmt.Errorf("org ID cannot be empty")
	}
	return store.ListOrgWorkflowDrafts(ctx, r.ORM(), orgID)
}

// AdoptTrigger re-owns trigger t.ID in place as t (t.UserID the new owner,
// disabled), only while previousOwner still owns it, under the new owner's
// org lock and only while each read still holds. With offerID it is that
// offer's acceptance, which must still be pending; any other pending offer
// of the trigger is revoked. One trigger_changes entry, operation
// "adopted", records the before and after state.
//
// An owner that changed since the decision is core.ErrTriggerOwnerChanged,
// a settled offer core.ErrTriggerOfferNotPending, a name the new owner
// already uses in that project core.ErrTriggerNameTaken; nothing is written.
func (r *Repo) AdoptTrigger(ctx context.Context, t *core.Trigger, previousOwner, offerID string, reads ...OrgRead) error {
	if t == nil || t.ID == "" || t.UserID == "" || previousOwner == "" {
		return fmt.Errorf("adopt trigger: trigger, new owner and previous owner are required")
	}
	if t.Enabled {
		return fmt.Errorf("adopt trigger: an adopted trigger starts disabled")
	}
	now := time.Now().UTC()
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		// The org lock first, then the trigger's row (store/org_lock.go).
		if err := store.LockUserOrgs(ctx, r.ORM(), t.UserID, reads...); err != nil {
			return err
		}
		if err := store.LockTrigger(ctx, r.ORM(), t.ID); err != nil {
			return err
		}
		before, err := store.GetTrigger(ctx, r.ORM(), t.ID)
		if err != nil {
			return err
		}
		if before.UserID != previousOwner {
			return core.ErrTriggerOwnerChanged
		}
		if err := store.AdoptTrigger(ctx, r.ORM(), t); err != nil {
			return mapTriggerNameConflict(err, t.Name)
		}
		if offerID != "" {
			settled, err := store.SettleTriggerOffer(ctx, r.ORM(), offerID, core.TriggerOfferAccepted, "", now)
			if err != nil {
				return err
			}
			if !settled {
				return core.ErrTriggerOfferNotPending
			}
		}
		if err := store.RevokeOtherTriggerOffers(ctx, r.ORM(), t.ID, offerID, now); err != nil {
			return err
		}
		after, err := store.GetTrigger(ctx, r.ORM(), t.ID)
		if err != nil {
			return err
		}
		return store.RecordTriggerChange(ctx, r.ORM(), "adopted", before, after)
	})
}

// CreateTriggerOffer stores a pending offer, superseding any offer of the
// same trigger still pending.
func (r *Repo) CreateTriggerOffer(ctx context.Context, o *core.TriggerOffer) error {
	if o == nil || o.ID == "" || o.TriggerID == "" {
		return fmt.Errorf("trigger offer: id and trigger are required")
	}
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.InsertTriggerOffer(ctx, r.ORM(), o)
	})
}

// GetTriggerOffer returns one offer, or core.ErrTriggerOfferNotFound.
func (r *Repo) GetTriggerOffer(ctx context.Context, id string) (*core.TriggerOffer, error) {
	if id == "" {
		return nil, core.ErrTriggerOfferNotFound
	}
	return store.GetTriggerOffer(ctx, r.ORM(), id)
}

// ListPendingTriggerOffers lists the pending offers f selects.
func (r *Repo) ListPendingTriggerOffers(ctx context.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error) {
	return store.ListPendingTriggerOffers(ctx, r.ORM(), f)
}

// SettleTriggerOffer moves a pending offer to status with reason; settled is
// false when it was no longer pending.
func (r *Repo) SettleTriggerOffer(ctx context.Context, id, status, reason string) (bool, error) {
	return store.SettleTriggerOffer(ctx, r.ORM(), id, status, reason, time.Now().UTC())
}
