// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for PrReviewWaitSubscription. The generated pr_review_wait_subscription_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: prReviewWaitSubscriptionRepo (a *crud.Repo[PrReviewWaitSubscription]) and the
//     package-level GetPrReviewWaitSubscriptionByID / ListPrReviewWaitSubscription / CreatePrReviewWaitSubscription / UpdatePrReviewWaitSubscription / DeletePrReviewWaitSubscription
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     PrReviewWaitSubscription struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindPrReviewWaitSubscriptionBy(ctx context.Context, db orm.Context, /* args */) ([]*PrReviewWaitSubscription, error) {
//		var rows []*PrReviewWaitSubscription
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("binding_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActivePrReviewWaitSubscriptions(ctx context.Context, db orm.Context) ([]*PrReviewWaitSubscription, error) {
//		return ListPrReviewWaitSubscription(ctx, db, orm.WhereEq("binding_id", true))
//	}
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

// PRReviewWaitStore implements the atomic subscription/inbox boundary.
type PRReviewWaitStore struct{ DB orm.Context }

// Binding delegates the existing authoritative original-author lookup.
func (s PRReviewWaitStore) Binding(ctx context.Context, o string, r, p int64) (prhandoff.Binding, error) {
	return (PRHandoffStore{DB: s.DB}).Binding(ctx, o, r, p)
}

// Arm commits a subscription before readiness can be exposed. Pre-arm inbox
// entries are intentionally retained and read by the first workflow Read.
func (s PRReviewWaitStore) Arm(ctx context.Context, r prreviewwait.Request, proof runcontinuity.Result) (prreviewwait.Subscription, error) {
	var result prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		b, err := s.Binding(ctx, r.OwnerID, r.RepoID, r.PR)
		if err != nil {
			return err
		}
		original := runcontinuity.Execution{WorkflowID: b.Author.RootID, RunID: b.Author.ExecutionID}
		if b.Author.OwnerID != r.OwnerID || b.Author.ChatID != r.ChatID || b.Pull.HeadSHA != r.HeadSHA || proof.Original != original || proof.Current != r.Execution || !proof.Live {
			return prhandoff.ErrRefused
		}
		hash := sha256.Sum256([]byte(b.ID + "\x00" + r.HeadSHA))
		id := hex.EncodeToString(hash[:])
		row := &PrReviewWaitSubscription{ID: id, BindingID: b.ID, HeadSha: r.HeadSHA, WorkflowID: original.WorkflowID, OriginalRunID: original.RunID, CurrentRunID: proof.Current.RunID, State: "armed"}
		if _, err := s.DB.Bun().NewInsert().Model(row).On("CONFLICT (binding_id,head_sha) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		existing, err := GetPrReviewWaitSubscriptionByID(ctx, s.DB, id)
		if err != nil {
			return err
		}
		if existing.WorkflowID != original.WorkflowID || existing.OriginalRunID != original.RunID || existing.State == "cancelled" || existing.State == "needs_human" {
			return prhandoff.ErrRefused
		}
		result = waitSub(existing)
		if result.CurrentRunID != proof.Current.RunID {
			result, err = s.advance(ctx, result, proof)
			return err
		}
		return nil
	})
	return result, err
}

func waitSub(r *PrReviewWaitSubscription) prreviewwait.Subscription {
	return prreviewwait.Subscription{ID: r.ID, BindingID: r.BindingID, HeadSHA: r.HeadSha, WorkflowID: r.WorkflowID, OriginalRunID: r.OriginalRunID, CurrentRunID: r.CurrentRunID, State: r.State, Ready: r.Ready, NotifiedSequence: r.NotifiedSequence, ConsumedSequence: r.ConsumedSequence}
}
func waitOutcome(r *PrReviewWaitOutcome) prreviewwait.Outcome {
	return prreviewwait.Outcome{ID: r.ID, BindingID: r.BindingID, HeadSHA: r.HeadSha, ProviderRef: r.ProviderRef, Sequence: r.ProviderSequence, State: r.State}
}

// Ready exposes only an already durable subscription, never PR readiness first.
func (s PRReviewWaitStore) Ready(ctx context.Context, sub prreviewwait.Subscription) error {
	_, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("ready = true").Where("id = ? AND current_run_id = ? AND state = 'armed'", sub.ID, sub.CurrentRunID).Exec(ctx)
	return err
}

// Read requires the exact execution cursor and current bound head. A repaired
// head cannot consume an earlier round's inbox.
func (s PRReviewWaitStore) Read(ctx context.Context, sub prreviewwait.Subscription) (prreviewwait.Outcome, error) {
	current, err := GetPrReviewWaitSubscriptionByID(ctx, s.DB, sub.ID)
	if err != nil {
		return prreviewwait.Outcome{}, err
	}
	if current.CurrentRunID != sub.CurrentRunID || current.WorkflowID != sub.WorkflowID {
		return prreviewwait.Outcome{}, prhandoff.ErrRefused
	}
	b, err := GetPrHandoffBindingByID(ctx, s.DB, current.BindingID)
	if err != nil {
		return prreviewwait.Outcome{}, err
	}
	if b.HeadSha != current.HeadSha {
		return prreviewwait.Outcome{State: "unavailable", HeadSHA: current.HeadSha}, nil
	}
	var row PrReviewWaitOutcome
	err = s.DB.Bun().NewSelect().Model(&row).Where("binding_id = ? AND head_sha = ?", current.BindingID, current.HeadSha).OrderExpr("provider_sequence DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return prreviewwait.Outcome{}, nil
	}
	if err != nil {
		return prreviewwait.Outcome{}, err
	}
	return waitOutcome(&row), nil
}

// Consume is separate from notification ack and never changes original author.
func (s PRReviewWaitStore) Consume(ctx context.Context, sub prreviewwait.Subscription, o prreviewwait.Outcome) error {
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		current, err := GetPrReviewWaitSubscriptionByID(ctx, s.DB, sub.ID)
		if err != nil {
			return err
		}
		if current.CurrentRunID != sub.CurrentRunID || current.WorkflowID != sub.WorkflowID || o.HeadSHA != current.HeadSha {
			return prhandoff.ErrRefused
		}
		if o.Sequence == 0 && o.State == "unavailable" {
			_, err = s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("state = 'needs_human'").Where("id = ?", sub.ID).Exec(ctx)
			return err
		}
		actual, err := GetPrReviewWaitOutcomeByID(ctx, s.DB, o.ID)
		if err != nil {
			return err
		}
		if actual.BindingID != current.BindingID || actual.HeadSha != current.HeadSha || actual.ProviderSequence != o.Sequence || actual.State != o.State || actual.ProviderRef != o.ProviderRef {
			return prhandoff.ErrRefused
		}
		binding, err := GetPrHandoffBindingByID(ctx, s.DB, current.BindingID)
		if err != nil {
			return err
		}
		if binding.HeadSha != current.HeadSha {
			return prhandoff.ErrRefused
		}
		var latest PrReviewWaitOutcome
		if err := s.DB.Bun().NewSelect().Model(&latest).Where("binding_id = ? AND head_sha = ?", current.BindingID, current.HeadSha).OrderExpr("provider_sequence DESC").Limit(1).Scan(ctx); err != nil {
			return err
		}
		if latest.ID != actual.ID {
			return prreviewwait.ErrStaleRead
		}
		_, err = s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("consumed_sequence = GREATEST(consumed_sequence, ?)", o.Sequence).Set("state = 'resolved'").Where("id = ?", sub.ID).Exec(ctx)
		return err
	})
}

// Cancel releases only this exact subscription; never restarts an author.
func (s PRReviewWaitStore) Cancel(ctx context.Context, sub prreviewwait.Subscription) error {
	_, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("state = 'cancelled'").Where("id = ? AND current_run_id = ? AND state = 'armed'", sub.ID, sub.CurrentRunID).Exec(ctx)
	return err
}

// Put persists provider-authorized, head-bound outcomes even before Arm.
func (s PRReviewWaitStore) Put(ctx context.Context, o prreviewwait.Outcome) (prreviewwait.Subscription, error) {
	var out prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		binding, err := GetPrHandoffBindingByID(ctx, s.DB, o.BindingID)
		if err != nil {
			return err
		}
		if binding.HeadSha != o.HeadSHA {
			return prhandoff.ErrRefused
		}
		row := &PrReviewWaitOutcome{ID: o.ID, BindingID: o.BindingID, HeadSha: o.HeadSHA, ProviderRef: o.ProviderRef, ProviderSequence: o.Sequence, State: o.State}
		if _, err = s.DB.Bun().NewInsert().Model(row).On("CONFLICT DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		persisted, err := GetPrReviewWaitOutcomeByID(ctx, s.DB, o.ID)
		if err != nil {
			return err
		}
		if persisted.ProviderSequence != o.Sequence || persisted.State != o.State {
			return prhandoff.ErrRefused
		}
		var sub PrReviewWaitSubscription
		err = s.DB.Bun().NewSelect().Model(&sub).Where("binding_id = ? AND head_sha = ?", o.BindingID, o.HeadSHA).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out = waitSub(&sub)
		return nil
	})
	return out, err
}

// Advance CASes the exact recipient only along a fully proven immutable chain.
func (s PRReviewWaitStore) Advance(ctx context.Context, sub prreviewwait.Subscription, p runcontinuity.Result) (prreviewwait.Subscription, error) {
	var out prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { var err error; out, err = s.advance(ctx, sub, p); return err })
	return out, err
}
func (s PRReviewWaitStore) advance(ctx context.Context, sub prreviewwait.Subscription, p runcontinuity.Result) (prreviewwait.Subscription, error) {
	if p.Original != (runcontinuity.Execution{WorkflowID: sub.WorkflowID, RunID: sub.OriginalRunID}) || !p.Live || p.Current.WorkflowID != sub.WorkflowID {
		return prreviewwait.Subscription{}, prhandoff.ErrRefused
	}
	valid := sub.CurrentRunID == p.Current.RunID || sub.CurrentRunID == p.Original.RunID
	for _, e := range p.Edges {
		if e.From.RunID == sub.CurrentRunID {
			valid = true
		}
	}
	if !valid {
		return prreviewwait.Subscription{}, prhandoff.ErrRefused
	}
	if sub.CurrentRunID == p.Current.RunID {
		return sub, nil
	}
	result, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("current_run_id = ?", p.Current.RunID).Set("notified_sequence = 0").Where("id = ? AND current_run_id = ? AND state = 'armed'", sub.ID, sub.CurrentRunID).Exec(ctx)
	if err != nil {
		return prreviewwait.Subscription{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return prreviewwait.Subscription{}, err
	}
	if n != 1 {
		return prreviewwait.Subscription{}, prhandoff.ErrRefused
	}
	sub.CurrentRunID = p.Current.RunID
	sub.NotifiedSequence = 0
	return sub, nil
}

// Notified acknowledges a successful exact-run signal, NOT author consumption.
func (s PRReviewWaitStore) Notified(ctx context.Context, sub prreviewwait.Subscription, sequence int64) error {
	_, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("notified_sequence = GREATEST(notified_sequence, ?)", sequence).Where("id = ? AND current_run_id = ?", sub.ID, sub.CurrentRunID).Exec(ctx)
	return err
}

// Unavailable persists an explicit needs-human outcome for terminal authors.
func (s PRReviewWaitStore) Unavailable(ctx context.Context, sub prreviewwait.Subscription) error {
	_, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("state = 'needs_human'").Where("id = ? AND current_run_id = ? AND state = 'armed'", sub.ID, sub.CurrentRunID).Exec(ctx)
	return err
}

// PrReviewWaitSubscriptionExtra carries fields on PrReviewWaitSubscription that are NOT columns — computed
// values, transient state, anything derived rather than stored. PrReviewWaitSubscription is
// embedded on PrReviewWaitSubscriptionExtra (see pr_review_wait_subscription_orm.go), so a field added here is a field on
// every PrReviewWaitSubscription value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type PrReviewWaitSubscriptionExtra struct {
}
