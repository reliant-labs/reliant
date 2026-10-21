// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatUpdate. The generated chat_update_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatUpdateRepo (a *crud.Repo[ChatUpdate]) and the
//     package-level GetChatUpdateByID / ListChatUpdate / CreateChatUpdate / UpdateChatUpdate / DeleteChatUpdate
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ChatUpdate struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindChatUpdateBy(ctx context.Context, db orm.Context, /* args */) ([]*ChatUpdate, error) {
//		var rows []*ChatUpdate
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChatUpdates(ctx context.Context, db orm.Context) ([]*ChatUpdate, error) {
//		return ListChatUpdate(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

// ChatUpdateExtra carries fields on ChatUpdate that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatUpdate is
// embedded on ChatUpdateExtra (see chat_update_orm.go), so a field added here is a field on
// every ChatUpdate value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatUpdateExtra struct {
}
