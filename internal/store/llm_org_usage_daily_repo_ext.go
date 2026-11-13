// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for LlmOrgUsageDaily. The generated llm_org_usage_daily_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: llmOrgUsageDailyRepo (a *crud.Repo[LlmOrgUsageDaily]) and the
//     package-level GetLlmOrgUsageDailyByPK / ListLlmOrgUsageDaily / CreateLlmOrgUsageDaily / UpsertLlmOrgUsageDaily, keyed by
//     the whole composite key (day, org_id, account_id, user_id, provider, model, funding, purpose), plus UpdateLlmOrgUsageDaily / DeleteLlmOrgUsageDailyByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     LlmOrgUsageDaily struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindLlmOrgUsageDailyBy(ctx context.Context, db orm.Context, /* args */) ([]*LlmOrgUsageDaily, error) {
//		var rows []*LlmOrgUsageDaily
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("calls = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveLlmOrgUsageDailies(ctx context.Context, db orm.Context) ([]*LlmOrgUsageDaily, error) {
//		return ListLlmOrgUsageDaily(ctx, db, orm.WhereEq("calls", true))
//	}
package store

// LlmOrgUsageDailyExtra carries fields on LlmOrgUsageDaily that are NOT columns — computed
// values, transient state, anything derived rather than stored. LlmOrgUsageDaily is
// embedded on LlmOrgUsageDailyExtra (see llm_org_usage_daily_orm.go), so a field added here is a field on
// every LlmOrgUsageDaily value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type LlmOrgUsageDailyExtra struct {
}
