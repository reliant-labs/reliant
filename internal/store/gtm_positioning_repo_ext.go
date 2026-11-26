// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for GtmPositioning. The generated gtm_positioning_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: gtmPositioningRepo (a *crud.Repo[GtmPositioning]) and the
//     package-level GetGtmPositioningByID / ListGtmPositioning / CreateGtmPositioning / UpdateGtmPositioning / DeleteGtmPositioning
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     GtmPositioning struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of GtmPositioningExtStore (gtm_positioning_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindGtmPositioningBy(ctx context.Context, db orm.Context, /* args */) ([]*GtmPositioning, error) {
//		var rows []*GtmPositioning
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("positioning_md = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveGtmPositionings(ctx context.Context, db orm.Context) ([]*GtmPositioning, error) {
//		return ListGtmPositioning(ctx, db, orm.WhereEq("positioning_md", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateGtmPositioningMasked per row (N round trips: it times out at scale). The
// generated UpdateGtmPositioningWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveGtmPositionings(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*GtmPositioning)(nil)).
//			Set("positioning_md = ?", to).
//			Set("updated_at = now()"). // a builder write is not the generated path: stamp it
//			Where("positioning_md = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

// GtmPositioningExtra carries fields on GtmPositioning that are NOT columns — computed
// values, transient state, anything derived rather than stored. GtmPositioning is
// embedded on GtmPositioningExtra (see gtm_positioning_orm.go), so a field added here is a field on
// every GtmPositioning value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type GtmPositioningExtra struct {
}
