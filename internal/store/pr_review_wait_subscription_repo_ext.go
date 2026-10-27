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
func (s PRReviewWaitStore) Arm(ctx context.Context, r prreviewwait.Request, proof runcontinuity.Result, authority prreviewwait.Authority) (prreviewwait.Subscription, error) {
	var result prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if len(r.RequestKey) != 64 || r.OwnerID == "" || r.ChatID == "" || r.Execution.WorkflowID == "" || r.Execution.RunID == "" {
			return prhandoff.ErrRefused
		}
		request := &PrReviewWaitRequest{ID: r.RequestKey, OwnerID: r.OwnerID, ChatID: r.ChatID, WorkflowID: r.Execution.WorkflowID, RunID: r.Execution.RunID, State: "arming"}
		if _, err := s.DB.Bun().NewInsert().Model(request).On("CONFLICT (id) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		persisted, err := GetPrReviewWaitRequestByID(ctx, s.DB, r.RequestKey)
		if err != nil {
			return err
		}
		if persisted.OwnerID != r.OwnerID || persisted.ChatID != r.ChatID || persisted.WorkflowID != r.Execution.WorkflowID || persisted.RunID != r.Execution.RunID || persisted.State == "cancelled" {
			return prhandoff.ErrRefused
		}
		b, err := s.Binding(ctx, r.OwnerID, r.RepoID, r.PR)
		if err != nil {
			return err
		}
		original := runcontinuity.Execution{WorkflowID: b.Author.RootID, RunID: b.Author.ExecutionID}
		if b.Author.OwnerID != r.OwnerID || b.Author.ChatID != r.ChatID || b.Pull.HeadSHA != r.HeadSHA || proof.Original != original || proof.Current != r.Execution || !proof.Live {
			return prhandoff.ErrRefused
		}
		if err := s.bindAuthority(ctx, b, authority); err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(b.ID + "\x00" + r.HeadSHA))
		id := hex.EncodeToString(hash[:])
		row := &PrReviewWaitSubscription{RequestKey: r.RequestKey, ID: id, BindingID: b.ID, HeadSha: r.HeadSHA, WorkflowID: original.WorkflowID, OriginalRunID: original.RunID, CurrentRunID: proof.Current.RunID, State: "armed"}
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
		if existing.CurrentRunID == proof.Current.RunID && existing.RequestKey != r.RequestKey {
			previousRequest, err := GetPrReviewWaitRequestByID(ctx, s.DB, existing.RequestKey)
			if err != nil {
				return err
			}
			// Cursor delivery may already have advanced; only a genuinely proven
			// successor execution may replace request ownership, never another wait
			// occurrence inside the same exact execution.
			predecessor := false
			for _, edge := range proof.Edges {
				if edge.From.WorkflowID == r.Execution.WorkflowID && edge.From.RunID == previousRequest.RunID {
					predecessor = true
				}
			}
			if previousRequest.RunID == r.Execution.RunID || previousRequest.WorkflowID != r.Execution.WorkflowID || previousRequest.OwnerID != r.OwnerID || previousRequest.ChatID != r.ChatID || !predecessor {
				return prhandoff.ErrRefused
			}
		}
		result = waitSub(existing)
		if result.CurrentRunID != proof.Current.RunID {
			result, err = s.advance(ctx, result, proof)
			if err != nil {
				return err
			}
		}
		if _, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("request_key = ?", r.RequestKey).Where("id = ? AND current_run_id = ?", id, r.Execution.RunID).Exec(ctx); err != nil {
			return err
		}
		if _, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitRequest)(nil)).Set("state = 'armed'").Set("subscription_id = ?", id).Where("id = ? AND state != 'cancelled'", r.RequestKey).Exec(ctx); err != nil {
			return err
		}
		result.RequestKey = r.RequestKey
		return nil
	})
	return result, err
}

func waitSub(r *PrReviewWaitSubscription) prreviewwait.Subscription {
	return prreviewwait.Subscription{RequestKey: r.RequestKey, ID: r.ID, BindingID: r.BindingID, HeadSHA: r.HeadSha, WorkflowID: r.WorkflowID, OriginalRunID: r.OriginalRunID, CurrentRunID: r.CurrentRunID, State: r.State, Ready: r.Ready, NotifiedSequence: r.NotifiedSequence, ConsumedSequence: r.ConsumedSequence}
}
func waitOutcome(r *PrReviewWaitOutcome) prreviewwait.Outcome {
	return prreviewwait.Outcome{AuthorityID: r.AuthorityID, ID: r.ID, BindingID: r.BindingID, HeadSHA: r.HeadSha, ProviderRef: r.ProviderRef, Sequence: r.ProviderSequence, State: r.State}
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
	authority, err := s.authority(ctx, current.BindingID, current.HeadSha)
	if err != nil {
		return prreviewwait.Outcome{}, err
	}
	var row PrReviewWaitOutcome
	err = s.DB.Bun().NewSelect().Model(&row).Where("binding_id = ? AND head_sha = ? AND authority_id = ?", current.BindingID, current.HeadSha, authority.AuthorityID).OrderExpr("provider_sequence DESC").Limit(1).Scan(ctx)
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
		if actual.BindingID != current.BindingID || actual.HeadSha != current.HeadSha || actual.ProviderSequence != o.Sequence || actual.State != o.State || actual.ProviderRef != o.ProviderRef || actual.AuthorityID != o.AuthorityID {
			return prhandoff.ErrRefused
		}
		binding, err := GetPrHandoffBindingByID(ctx, s.DB, current.BindingID)
		if err != nil {
			return err
		}
		if binding.HeadSha != current.HeadSha {
			return prhandoff.ErrRefused
		}
		authority, err := s.authority(ctx, current.BindingID, current.HeadSha)
		if err != nil {
			return err
		}
		if actual.AuthorityID != authority.AuthorityID {
			return prhandoff.ErrRefused
		}
		var latest PrReviewWaitOutcome
		if err := s.DB.Bun().NewSelect().Model(&latest).Where("binding_id = ? AND head_sha = ? AND authority_id = ?", current.BindingID, current.HeadSha, authority.AuthorityID).OrderExpr("provider_sequence DESC").Limit(1).Scan(ctx); err != nil {
			return err
		}
		if latest.ID != actual.ID {
			return prreviewwait.ErrStaleRead
		}
		_, err = s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("consumed_sequence = GREATEST(consumed_sequence, ?)", o.Sequence).Set("state = 'resolved'").Where("id = ?", sub.ID).Exec(ctx)
		return err
	})
}

// CancelRequest inserts an exact-author tombstone even when Arm's result is
// unknown. Serializable ordering ensures either Arm is cancelled after commit,
// or a late Arm sees cancellation and cannot recreate an orphan subscription.
func (s PRReviewWaitStore) CancelRequest(ctx context.Context, r prreviewwait.Request) error {
	if len(r.RequestKey) != 64 || r.OwnerID == "" || r.ChatID == "" || r.Execution.WorkflowID == "" || r.Execution.RunID == "" {
		return prhandoff.ErrRefused
	}
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		row := &PrReviewWaitRequest{ID: r.RequestKey, OwnerID: r.OwnerID, ChatID: r.ChatID, WorkflowID: r.Execution.WorkflowID, RunID: r.Execution.RunID, State: "cancelled"}
		if _, err := s.DB.Bun().NewInsert().Model(row).On("CONFLICT (id) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		existing, err := GetPrReviewWaitRequestByID(ctx, s.DB, r.RequestKey)
		if err != nil {
			return err
		}
		if existing.OwnerID != r.OwnerID || existing.ChatID != r.ChatID || existing.WorkflowID != r.Execution.WorkflowID || existing.RunID != r.Execution.RunID {
			return prhandoff.ErrRefused
		}
		if _, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitRequest)(nil)).Set("state = 'cancelled'").Where("id = ?", r.RequestKey).Exec(ctx); err != nil {
			return err
		}
		if existing.SubscriptionID == nil {
			return nil
		}
		_, err = s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("state = 'cancelled'").Where("id = ? AND request_key = ? AND workflow_id = ? AND current_run_id = ? AND state = 'armed'", *existing.SubscriptionID, r.RequestKey, r.Execution.WorkflowID, r.Execution.RunID).Exec(ctx)
		return err
	})
}

// Cancel releases only this exact subscription; never restarts an author.
func (s PRReviewWaitStore) Cancel(ctx context.Context, sub prreviewwait.Subscription) error {
	_, err := s.DB.Bun().NewUpdate().Model((*PrReviewWaitSubscription)(nil)).Set("state = 'cancelled'").Where("id = ? AND current_run_id = ? AND state = 'armed'", sub.ID, sub.CurrentRunID).Exec(ctx)
	return err
}

// Put persists provider-authorized, head-bound outcomes even before Arm.
func (s PRReviewWaitStore) Put(ctx context.Context, o prreviewwait.Outcome, authority prreviewwait.Authority) (prreviewwait.Subscription, error) {
	var out prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		binding, err := s.BindingByID(ctx, o.OwnerID, o.BindingID)
		if err != nil {
			return err
		}
		if binding.Pull.HeadSHA != o.HeadSHA || o.AuthorityID != authority.ID {
			return prhandoff.ErrRefused
		}
		if err := s.bindAuthority(ctx, binding, authority); err != nil {
			return err
		}
		row := &PrReviewWaitOutcome{AuthorityID: o.AuthorityID, ID: o.ID, BindingID: o.BindingID, HeadSha: o.HeadSHA, ProviderRef: o.ProviderRef, ProviderSequence: o.Sequence, State: o.State}
		if _, err = s.DB.Bun().NewInsert().Model(row).On("CONFLICT DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		persisted, err := GetPrReviewWaitOutcomeByID(ctx, s.DB, o.ID)
		if err != nil {
			return err
		}
		if persisted.ProviderSequence != o.Sequence || persisted.State != o.State || persisted.AuthorityID != o.AuthorityID || persisted.BindingID != o.BindingID || persisted.HeadSha != o.HeadSHA || persisted.ProviderRef != o.ProviderRef {
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
