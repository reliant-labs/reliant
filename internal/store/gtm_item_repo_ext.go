// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for GtmItem. The generated gtm_item_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: gtmItemRepo (a *crud.Repo[GtmItem]) and the
//     package-level GetGtmItemByID / ListGtmItem / CreateGtmItem / UpdateGtmItem / DeleteGtmItem
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     GtmItem struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of GtmItemExtStore (gtm_item_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindGtmItemBy(ctx context.Context, db orm.Context, /* args */) ([]*GtmItem, error) {
//		var rows []*GtmItem
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveGtmItems(ctx context.Context, db orm.Context) ([]*GtmItem, error) {
//		return ListGtmItem(ctx, db, orm.WhereEq("org_id", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateGtmItemMasked per row (N round trips: it times out at scale). The
// generated UpdateGtmItemWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveGtmItems(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*GtmItem)(nil)).
//			Set("org_id = ?", to).
//			Set("updated_at = now()"). // a builder write is not the generated path: stamp it
//			Where("org_id = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

// GtmItemExtra carries fields on GtmItem that are NOT columns — computed
// values, transient state, anything derived rather than stored. GtmItem is
// embedded on GtmItemExtra (see gtm_item_orm.go), so a field added here is a field on
// every GtmItem value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type GtmItemExtra struct {
}
