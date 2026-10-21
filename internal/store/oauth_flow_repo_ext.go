// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for OauthFlow. The generated oauth_flow_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: oauthFlowRepo (a *crud.Repo[OauthFlow]) and the
//     package-level GetOauthFlowByID / ListOauthFlow / CreateOauthFlow / UpdateOauthFlow / DeleteOauthFlow
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     OauthFlow struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindOauthFlowBy(ctx context.Context, db orm.Context, /* args */) ([]*OauthFlow, error) {
//		var rows []*OauthFlow
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveOauthFlows(ctx context.Context, db orm.Context) ([]*OauthFlow, error) {
//		return ListOauthFlow(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

// OauthFlowExtra carries fields on OauthFlow that are NOT columns — computed
// values, transient state, anything derived rather than stored. OauthFlow is
// embedded on OauthFlowExtra (see oauth_flow_orm.go), so a field added here is a field on
// every OauthFlow value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type OauthFlowExtra struct {
}
