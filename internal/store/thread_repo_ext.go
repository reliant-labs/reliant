// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Thread. The generated thread_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: threadRepo (a *crud.Repo[Thread]) and the
//     package-level GetThreadByID / ListThread / CreateThread / UpdateThread / DeleteThread
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Thread struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindThreadBy(ctx context.Context, db orm.Context, /* args */) ([]*Thread, error) {
//		var rows []*Thread
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveThreads(ctx context.Context, db orm.Context) ([]*Thread, error) {
//		return ListThread(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

// ThreadExtra carries fields on Thread that are NOT columns — computed
// values, transient state, anything derived rather than stored. Thread is
// embedded on ThreadExtra (see thread_orm.go), so a field added here is a field on
// every Thread value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ThreadExtra struct {
}
