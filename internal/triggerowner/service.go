// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package triggerowner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the triggerowner package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	Tx       TxRunner
	Locks    Locks
	Triggers Triggers
	Offers   Offers
	OrgReads OrgReads

	// Now is the clock offers and changes are stamped by; nil is time.Now.
	//
	// forge:optional-dep
	Now func() time.Time
}

// txPolicy is the level both owner-invariant writes run at. READ COMMITTED
// is correct because the trigger's row lock (and the new owner's org lock)
// serialize the writers, not snapshot isolation: under SERIALIZABLE the
// loser of a lock wait would fail with 40001 instead of re-reading the row
// the winner changed. WithRetryOn keeps the codes the facade's retry ladder
// also retried (40002, 25P02 and a COMMIT that rolled back); 23505 is
// deliberately not retried, a name conflict is the caller's answer.
var txPolicy = orm.ReadCommitted.WithRetryOn(func(err error) bool {
	return orm.RetryOnSQLState("40002", "25P02")(err) || orm.RetryOnCommitRollback(err)
})

// The package is the one writer of a trigger's owner after it is created:
// the takeover of a departed member's automations (control-plane
// docs/design/org-sharing.md §8). Every write re-checks, under the trigger's
// row lock, that the owner the decision was made on still owns it.

//forge:contract
type service struct {
	deps Deps
}

// New creates a new triggerowner service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Tx == nil || deps.Locks == nil || deps.Triggers == nil || deps.Offers == nil || deps.OrgReads == nil {
		return nil, errors.New("triggerowner: Deps.Tx, Locks, Triggers, Offers and OrgReads are required")
	}
	return &service{deps: deps}, nil
}

func (s *service) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now().UTC()
	}
	return time.Now().UTC()
}

// ListOrgTriggers lists every trigger of one org, whoever owns it. Only the
// takeover's admin read calls it; the handler decides who may.
func (s *service) ListOrgTriggers(ctx context.Context, orgID string) ([]*core.Trigger, error) {
	if orgID == "" {
		return nil, fmt.Errorf("org ID cannot be empty")
	}
	return s.deps.OrgReads.ListOrgTriggers(ctx, orgID)
}

// ListOrgWorkflowDrafts lists every saved workflow of one org, whoever owns
// it. Only the takeover's admin read calls it.
func (s *service) ListOrgWorkflowDrafts(ctx context.Context, orgID string) ([]*core.WorkflowDraft, error) {
	if orgID == "" {
		return nil, fmt.Errorf("org ID cannot be empty")
	}
	return s.deps.OrgReads.ListOrgWorkflowDrafts(ctx, orgID)
}

// AdoptTrigger re-owns trigger t.ID in place as t (t.UserID the new owner,
// disabled), only while previousOwner still owns it, under the new owner's
// org lock and only while each read still holds. With offerID it is that
// offer's acceptance, which must still be pending; any other pending offer
// of the trigger is revoked. One trigger_changes entry, operation "adopted",
// records the before and after state.
//
// An owner that changed since the decision is core.ErrTriggerOwnerChanged, a
// settled offer core.ErrTriggerOfferNotPending, a name the new owner already
// uses in that project core.ErrTriggerNameTaken; nothing is written.
func (s *service) AdoptTrigger(ctx context.Context, t *core.Trigger, previousOwner, offerID string, reads ...store.OrgRead) error {
	if t == nil || t.ID == "" || t.UserID == "" || previousOwner == "" {
		return fmt.Errorf("adopt trigger: trigger, new owner and previous owner are required")
	}
	if t.Enabled {
		return fmt.Errorf("adopt trigger: an adopted trigger starts disabled")
	}
	now := s.now()
	return s.deps.Tx.RunTx(ctx, txPolicy, func(ctx context.Context) error {
		// The org lock first, then the project, then the trigger's row
		// (store/org_lock.go); the project refuses adoption while it is
		// scheduled for deletion.
		if err := s.deps.Locks.LockUserOrgs(ctx, t.UserID, reads...); err != nil {
			return err
		}
		if err := s.deps.Locks.LockProjectsForWrite(ctx, t.ProjectID); err != nil {
			return err
		}
		if err := s.deps.Locks.LockTrigger(ctx, t.ID); err != nil {
			return err
		}
		before, err := s.deps.Triggers.GetTrigger(ctx, t.ID)
		if err != nil {
			return err
		}
		if before.UserID != previousOwner {
			return core.ErrTriggerOwnerChanged
		}
		if err := s.deps.Triggers.AdoptTrigger(ctx, t); err != nil {
			return mapTriggerNameConflict(err, t.Name)
		}
		if offerID != "" {
			settled, err := s.deps.Offers.SettleTriggerOffer(ctx, offerID, core.TriggerOfferAccepted, "", now)
			if err != nil {
				return err
			}
			if !settled {
				return core.ErrTriggerOfferNotPending
			}
		}
		if err := s.deps.Offers.RevokeOtherTriggerOffers(ctx, t.ID, offerID, now); err != nil {
			return err
		}
		after, err := s.deps.Triggers.GetTrigger(ctx, t.ID)
		if err != nil {
			return err
		}
		return s.deps.Triggers.RecordTriggerChange(ctx, "adopted", before, after)
	})
}

// CreateTriggerOffer stores a pending offer, superseding any offer of the
// same trigger still pending. The trigger's row lock serializes offers of one
// trigger (and an offer against an adoption, which takes the same lock), so
// two concurrent offers both complete and exactly one stays pending: the
// later one, which supersedes the first. A trigger re-owned since the offer
// was decided on is core.ErrTriggerOwnerChanged.
func (s *service) CreateTriggerOffer(ctx context.Context, o *core.TriggerOffer) error {
	if o == nil || o.ID == "" || o.TriggerID == "" {
		return fmt.Errorf("trigger offer: id and trigger are required")
	}
	return s.deps.Tx.RunTx(ctx, txPolicy, func(ctx context.Context) error {
		if err := s.deps.Locks.LockTrigger(ctx, o.TriggerID); err != nil {
			return err
		}
		current, err := s.deps.Triggers.GetTrigger(ctx, o.TriggerID)
		if err != nil {
			return err
		}
		if current.UserID != o.PreviousOwnerUserID {
			return core.ErrTriggerOwnerChanged
		}
		return s.deps.Offers.InsertTriggerOffer(ctx, o)
	})
}

// GetTriggerOffer returns one offer, or core.ErrTriggerOfferNotFound.
func (s *service) GetTriggerOffer(ctx context.Context, id string) (*core.TriggerOffer, error) {
	if id == "" {
		return nil, core.ErrTriggerOfferNotFound
	}
	return s.deps.Offers.GetTriggerOffer(ctx, id)
}

// ListPendingTriggerOffers lists the pending offers f selects.
func (s *service) ListPendingTriggerOffers(ctx context.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error) {
	return s.deps.Offers.ListPendingTriggerOffers(ctx, f)
}

// SettleTriggerOffer moves a pending offer to status with reason; settled is
// false when it was no longer pending.
func (s *service) SettleTriggerOffer(ctx context.Context, id, status, reason string) (bool, error) {
	return s.deps.Offers.SettleTriggerOffer(ctx, id, status, reason, s.now())
}

// mapTriggerNameConflict turns a unique violation on the trigger's
// (user, project, name) key into core.ErrTriggerNameTaken. The PgError is not
// wrapped (%w), so a retry ladder never re-runs a permanent conflict.
func mapTriggerNameConflict(err error, name string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == store.TriggerConstraintUserIDProjectIDNameKey {
		return fmt.Errorf("%w: %q", core.ErrTriggerNameTaken, name)
	}
	return err
}
