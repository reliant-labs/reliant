// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for AccessToken. The generated access_token_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: accessTokenRepo (a *crud.Repo[AccessToken]) and the
//     package-level GetAccessTokenByID / ListAccessToken / CreateAccessToken / UpdateAccessToken / DeleteAccessToken
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     AccessToken struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindAccessTokenBy(ctx context.Context, db orm.Context, /* args */) ([]*AccessToken, error) {
//		var rows []*AccessToken
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveAccessTokens(ctx context.Context, db orm.Context) ([]*AccessToken, error) {
//		return ListAccessToken(ctx, db, orm.WhereEq("org_id", true))
//	}
package store

// AccessTokenExtra carries fields on AccessToken that are NOT columns — computed
// values, transient state, anything derived rather than stored. AccessToken is
// embedded on AccessTokenExtra (see access_token_orm.go), so a field added here is a field on
// every AccessToken value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type AccessTokenExtra struct {
}
