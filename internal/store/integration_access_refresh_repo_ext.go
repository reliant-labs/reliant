// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for IntegrationAccessRefresh. The generated integration_access_refresh_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: integrationAccessRefreshRepo (a *crud.Repo[IntegrationAccessRefresh]) and the
//     package-level GetIntegrationAccessRefreshByPK / ListIntegrationAccessRefresh / CreateIntegrationAccessRefresh / UpsertIntegrationAccessRefresh, keyed by
//     the whole composite key (user_id, integration_id), plus UpdateIntegrationAccessRefresh / DeleteIntegrationAccessRefreshByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     IntegrationAccessRefresh struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindIntegrationAccessRefreshBy(ctx context.Context, db orm.Context, /* args */) ([]*IntegrationAccessRefresh, error) {
//		var rows []*IntegrationAccessRefresh
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("refreshed_at = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveIntegrationAccessRefreshes(ctx context.Context, db orm.Context) ([]*IntegrationAccessRefresh, error) {
//		return ListIntegrationAccessRefresh(ctx, db, orm.WhereEq("refreshed_at", true))
//	}
package store

// IntegrationAccessRefreshExtra carries fields on IntegrationAccessRefresh that are NOT columns — computed
// values, transient state, anything derived rather than stored. IntegrationAccessRefresh is
// embedded on IntegrationAccessRefreshExtra (see integration_access_refresh_orm.go), so a field added here is a field on
// every IntegrationAccessRefresh value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type IntegrationAccessRefreshExtra struct {
}
