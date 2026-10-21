// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for IntegrationEventAccess. The generated integration_event_access_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: integrationEventAccessRepo (a *crud.Repo[IntegrationEventAccess]) and the
//     package-level GetIntegrationEventAccessByPK / ListIntegrationEventAccess / CreateIntegrationEventAccess / UpsertIntegrationEventAccess, keyed by
//     the whole composite key (user_id, integration_id, account_key, resource_key), plus UpdateIntegrationEventAccess / DeleteIntegrationEventAccessByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     IntegrationEventAccess struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindIntegrationEventAccessBy(ctx context.Context, db orm.Context, /* args */) ([]*IntegrationEventAccess, error) {
//		var rows []*IntegrationEventAccess
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("resource_label = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveIntegrationEventAccesses(ctx context.Context, db orm.Context) ([]*IntegrationEventAccess, error) {
//		return ListIntegrationEventAccess(ctx, db, orm.WhereEq("resource_label", true))
//	}
package store

// IntegrationEventAccessExtra carries fields on IntegrationEventAccess that are NOT columns — computed
// values, transient state, anything derived rather than stored. IntegrationEventAccess is
// embedded on IntegrationEventAccessExtra (see integration_event_access_orm.go), so a field added here is a field on
// every IntegrationEventAccess value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type IntegrationEventAccessExtra struct {
}
