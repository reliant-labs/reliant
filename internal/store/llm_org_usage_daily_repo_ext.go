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

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// ListLLMOrgUsageDaily returns orgID's rollup rows of ORG-FUNDED calls for
// the UTC days from..to inclusive, oldest first. It is the only thing an org
// admin's usage read touches: the table holds nothing but org_key, org_seat
// and org_wallet calls (its funding CHECK), so no filter here can let a
// member's personal-account call through, and none needs to keep one out.
func ListLLMOrgUsageDaily(ctx context.Context, db orm.Context, orgID string, from, to time.Time) ([]*LlmOrgUsageDaily, error) {
	if orgID == "" {
		return nil, errors.New("org llm usage: org id is required")
	}
	var rows []*LlmOrgUsageDaily
	err := db.Bun().NewSelect().Model(&rows).
		Where("org_id = ?", orgID).
		Where("day >= ?::date", from.UTC().Format("2006-01-02")).
		Where("day <= ?::date", to.UTC().Format("2006-01-02")).
		OrderExpr("day, user_id, account_id, provider, model, funding, purpose").
		Scan(ctx)
	return rows, err
}

// OrgKeyModelUsage is the tokens one model used through an org's own API keys.
type OrgKeyModelUsage struct {
	Provider         string `bun:"provider"`
	Model            string `bun:"model"`
	Calls            int64  `bun:"calls"`
	InputTokens      int64  `bun:"input_tokens"`
	OutputTokens     int64  `bun:"output_tokens"`
	CacheReadTokens  int64  `bun:"cache_read_tokens"`
	CacheWriteTokens int64  `bun:"cache_write_tokens"`
}

// SumLLMOrgKeyUsage sums orgID's calls on its own API keys (funding org_key)
// per provider and model, over the UTC days from..to inclusive: what
// control-plane prices at list for the cost summary's byok source.
func SumLLMOrgKeyUsage(ctx context.Context, db orm.Context, orgID string, from, to time.Time) ([]OrgKeyModelUsage, error) {
	if orgID == "" {
		return nil, errors.New("org llm usage: org id is required")
	}
	var rows []OrgKeyModelUsage
	err := db.Bun().NewRaw(`SELECT provider, model, sum(calls)::bigint AS calls, sum(input_tokens)::bigint AS input_tokens,
		sum(output_tokens)::bigint AS output_tokens, sum(cache_read_tokens)::bigint AS cache_read_tokens,
		sum(cache_write_tokens)::bigint AS cache_write_tokens
		FROM llm_org_usage_daily
		WHERE org_id = ? AND funding = 'org_key' AND day >= ?::date AND day <= ?::date
		GROUP BY provider, model ORDER BY provider, model`,
		orgID, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")).Scan(ctx, &rows)
	return rows, err
}

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
