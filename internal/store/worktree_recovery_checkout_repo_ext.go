// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for WorktreeRecoveryCheckout. The generated worktree_recovery_checkout_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: worktreeRecoveryCheckoutRepo (a *crud.Repo[WorktreeRecoveryCheckout]) and the
//     package-level GetWorktreeRecoveryCheckoutByPK / ListWorktreeRecoveryCheckout / CreateWorktreeRecoveryCheckout / UpsertWorktreeRecoveryCheckout, keyed by
//     the whole composite key (recovery_id, repo_id), plus UpdateWorktreeRecoveryCheckout / DeleteWorktreeRecoveryCheckoutByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     WorktreeRecoveryCheckout struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindWorktreeRecoveryCheckoutBy(ctx context.Context, db orm.Context, /* args */) ([]*WorktreeRecoveryCheckout, error) {
//		var rows []*WorktreeRecoveryCheckout
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("rel = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveWorktreeRecoveryCheckouts(ctx context.Context, db orm.Context) ([]*WorktreeRecoveryCheckout, error) {
//		return ListWorktreeRecoveryCheckout(ctx, db, orm.WhereEq("rel", true))
//	}
package store

// WorktreeRecoveryCheckoutExtra carries fields on WorktreeRecoveryCheckout that are NOT columns — computed
// values, transient state, anything derived rather than stored. WorktreeRecoveryCheckout is
// embedded on WorktreeRecoveryCheckoutExtra (see worktree_recovery_checkout_orm.go), so a field added here is a field on
// every WorktreeRecoveryCheckout value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorktreeRecoveryCheckoutExtra struct {
}
