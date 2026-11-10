// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for CheckoutTeardown. The generated checkout_teardown_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: checkoutTeardownRepo (a *crud.Repo[CheckoutTeardown]) and the
//     package-level GetCheckoutTeardownByID / ListCheckoutTeardown / CreateCheckoutTeardown / UpdateCheckoutTeardown / DeleteCheckoutTeardown
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     CheckoutTeardown struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindCheckoutTeardownBy(ctx context.Context, db orm.Context, /* args */) ([]*CheckoutTeardown, error) {
//		var rows []*CheckoutTeardown
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("project_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveCheckoutTeardowns(ctx context.Context, db orm.Context) ([]*CheckoutTeardown, error) {
//		return ListCheckoutTeardown(ctx, db, orm.WhereEq("project_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Checkout teardown states (the state column; checkout_teardowns migration).
const (
	CheckoutTeardownPending = "pending"
	CheckoutTeardownDone    = "done"
	CheckoutTeardownFailed  = "failed"
	CheckoutTeardownNotRun  = "not_run"
	CheckoutTeardownExpired = "expired"
)

// PendingCheckoutTeardown returns the checkout's unsettled teardown, or nil.
// There is at most one (checkout_teardowns_one_pending). forUpdate locks the
// row for the caller's transaction.
func PendingCheckoutTeardown(ctx context.Context, db orm.Context, projectID, daemonID string, forUpdate bool) (*CheckoutTeardown, error) {
	row := new(CheckoutTeardown)
	q := db.Bun().NewSelect().Model(row).
		Where("project_id = ? AND daemon_id = ? AND state = ?", projectID, daemonID, CheckoutTeardownPending)
	if forUpdate {
		q = q.For("UPDATE")
	}
	if err := q.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the checkout's pending teardown: %w", err)
	}
	return row, nil
}

// LockCheckoutTeardown returns the teardown id, locked for the caller's
// transaction, or nil when there is none.
func LockCheckoutTeardown(ctx context.Context, db orm.Context, id string) (*CheckoutTeardown, error) {
	row := new(CheckoutTeardown)
	if err := db.Bun().NewSelect().Model(row).Where("id = ?", id).For("UPDATE").Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read checkout teardown %s: %w", id, err)
	}
	return row, nil
}

// SettleCheckoutTeardownRow moves a pending teardown to state with detail.
func SettleCheckoutTeardownRow(ctx context.Context, db orm.Context, id, state, detail string, now time.Time) error {
	_, err := db.Bun().NewUpdate().Model((*CheckoutTeardown)(nil)).
		Set("state = ?", state).
		Set("detail = ?", detail).
		Set("updated_at = ?", now).
		Set("settled_at = ?", now).
		Where("id = ? AND state = ?", id, CheckoutTeardownPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to settle checkout teardown %s: %w", id, err)
	}
	return nil
}

// CheckoutTeardownExtra carries fields on CheckoutTeardown that are NOT columns — computed
// values, transient state, anything derived rather than stored. CheckoutTeardown is
// embedded on CheckoutTeardownExtra (see checkout_teardown_orm.go), so a field added here is a field on
// every CheckoutTeardown value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type CheckoutTeardownExtra struct {
	// Reused is set by db.BeginCheckoutTeardown on a pending op it returned
	// for a retry instead of recording a new one: the caller must learn how
	// that op ended before sending it again.
	Reused bool `bun:"-"`
}
