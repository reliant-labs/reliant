// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for EndTenant. The generated end_tenant_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: endTenantRepo (a *crud.Repo[EndTenant]) and the
//     package-level GetEndTenantByID / ListEndTenant / CreateEndTenant / UpdateEndTenant / DeleteEndTenant
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     EndTenant struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of EndTenantExtStore (end_tenant_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindEndTenantBy(ctx context.Context, db orm.Context, /* args */) ([]*EndTenant, error) {
//		var rows []*EndTenant
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("host_org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveEndTenants(ctx context.Context, db orm.Context) ([]*EndTenant, error) {
//		return ListEndTenant(ctx, db, orm.WhereEq("host_org_id", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateEndTenantMasked per row (N round trips: it times out at scale). The
// generated UpdateEndTenantWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveEndTenants(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*EndTenant)(nil)).
//			Set("host_org_id = ?", to).
//			Set("updated_at = now()"). // a builder write is not the generated path: stamp it
//			Where("host_org_id = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

// EndTenantExtra carries fields on EndTenant that are NOT columns — computed
// values, transient state, anything derived rather than stored. EndTenant is
// embedded on EndTenantExtra (see end_tenant_orm.go), so a field added here is a field on
// every EndTenant value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type EndTenantExtra struct {
}
