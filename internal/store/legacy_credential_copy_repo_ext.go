// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for LegacyCredentialCopy. The generated legacy_credential_copy_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: legacyCredentialCopyRepo (a *crud.Repo[LegacyCredentialCopy]) and the
//     package-level GetLegacyCredentialCopyByID / ListLegacyCredentialCopy / CreateLegacyCredentialCopy / UpdateLegacyCredentialCopy / DeleteLegacyCredentialCopy
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     LegacyCredentialCopy struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindLegacyCredentialCopyBy(ctx context.Context, db orm.Context, /* args */) ([]*LegacyCredentialCopy, error) {
//		var rows []*LegacyCredentialCopy
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("completed_at = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveLegacyCredentialCopies(ctx context.Context, db orm.Context) ([]*LegacyCredentialCopy, error) {
//		return ListLegacyCredentialCopy(ctx, db, orm.WhereEq("completed_at", true))
//	}
package store

// LegacyCredentialCopyExtra carries fields on LegacyCredentialCopy that are NOT columns — computed
// values, transient state, anything derived rather than stored. LegacyCredentialCopy is
// embedded on LegacyCredentialCopyExtra (see legacy_credential_copy_orm.go), so a field added here is a field on
// every LegacyCredentialCopy value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type LegacyCredentialCopyExtra struct {
}
