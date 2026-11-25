package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Reads of the insights rollups (insights_rollup.go) for Agents › Insights
// (dev-docs/design/agent-analytics.md §3.6, PR C1). A team read touches only
// the team tables, which name no one; a member read is one member's rows of
// the member tables. The one team-side read of a member table is
// ListInsightsContributors, which answers opaque per-response ordinals, never
// a user id, so the small-group floor can count people without holding them.

// InsightsDim is a dimension an insights read groups by: a closed set, each
// mapped to a column expression of the rollup tables, never interpolated
// from a caller's string.
type InsightsDim string

const (
	InsightsDimDay      InsightsDim = "day"
	InsightsDimProject  InsightsDim = "project"
	InsightsDimWorkflow InsightsDim = "workflow"
	InsightsDimPreset   InsightsDim = "preset"
	InsightsDimModel    InsightsDim = "model"
	InsightsDimProvider InsightsDim = "provider"
	InsightsDimPurpose  InsightsDim = "purpose"
	InsightsDimTool     InsightsDim = "tool"
	InsightsDimToolKind InsightsDim = "tool_kind"
)

// InsightsSource is which calls a read sums: model calls or tool calls.
type InsightsSource int

const (
	InsightsModelCalls InsightsSource = iota + 1
	InsightsToolCalls
)

// insightsDimExpr is each dimension's key expression, by source. A dimension
// a source does not have is absent: InsightsDimApplies.
var insightsDimExpr = map[InsightsSource]map[InsightsDim]string{
	InsightsModelCalls: {
		InsightsDimDay:      `to_char(day, 'YYYY-MM-DD')`,
		InsightsDimProject:  `project_id`,
		InsightsDimWorkflow: `workflow_name`,
		InsightsDimPreset:   `preset`,
		InsightsDimModel:    `model`,
		InsightsDimProvider: `provider`,
		InsightsDimPurpose:  `purpose`,
	},
	InsightsToolCalls: {
		InsightsDimDay:      `to_char(day, 'YYYY-MM-DD')`,
		InsightsDimProject:  `project_id`,
		InsightsDimWorkflow: `workflow_name`,
		InsightsDimPreset:   `preset`,
		InsightsDimTool:     `tool_name`,
		InsightsDimToolKind: `tool_kind`,
	},
}

// InsightsDimApplies reports whether src's calls can be grouped by dim.
func InsightsDimApplies(src InsightsSource, dim InsightsDim) bool {
	_, ok := insightsDimExpr[src][dim]
	return ok
}

// Who paid, as the model rollups record it. Cost is read only for calls the
// row's org provably paid for itself (§3.6, D6): paid_by_org, a key column of
// both model tables, set when the call was folded only if its funding was
// org_key, org_seat or org_wallet AND its payer (llm_call_usage.funding_org_id)
// was the row's org. Every org-spend read below filters on it, on the member
// table as on the team table: the member table keeps a member's cost whatever
// paid, so without the filter a drill-down or "My analytics" would count
// another org's payment, or one whose payer is unknown, as this org's spend.
const (
	insightsPaidByOrg = `paid_by_org`
	// insightsPayerUnconfirmed is an org-class funding this org is not known
	// to have paid: the payer is unknown (the org-less managed key, a row
	// written before the payer was recorded) or another org.
	insightsPayerUnconfirmed = `funding IN ('org_key', 'org_seat', 'org_wallet') AND NOT paid_by_org`
	insightsPersonalFunded   = `funding IN ('personal', 'local')`
	insightsUnrecordedFunded = `funding = ''`
)

// InsightsModelRow is the sum of one group's model-call rollup rows.
// Histograms have InsightsLatencyBuckets counts. The cost columns count only
// calls the org provably paid for (paid_by_org).
type InsightsModelRow struct {
	Key                    string  `bun:"key"`
	Calls                  int64   `bun:"calls"`
	RetriedCalls           int64   `bun:"retried_calls"`
	ErrorCalls             int64   `bun:"error_calls"`
	CancelledCalls         int64   `bun:"cancelled_calls"`
	InputTokens            int64   `bun:"input_tokens"`
	OutputTokens           int64   `bun:"output_tokens"`
	CacheReadTokens        int64   `bun:"cache_read_tokens"`
	CacheWriteTokens       int64   `bun:"cache_write_tokens"`
	LatencyMsTotal         int64   `bun:"latency_ms_total"`
	LatencyMsHist          []int64 `bun:"latency_ms_hist,array"`
	TTFTMsTotal            int64   `bun:"ttft_ms_total"`
	TTFTMsHist             []int64 `bun:"ttft_ms_hist,array"`
	OrgFundedCalls         int64   `bun:"org_funded_calls"`
	PayerUnconfirmedCalls  int64   `bun:"payer_unconfirmed_calls"`
	PersonallyFundedCalls  int64   `bun:"personally_funded_calls"`
	UnrecordedFundingCalls int64   `bun:"unrecorded_funding_calls"`
	ReportedCalls          int64   `bun:"reported_calls"`
	ReportedCostUSDNanos   int64   `bun:"reported_cost_usd_nanos"`
	EstimatedCalls         int64   `bun:"estimated_calls"`
	EstimatedCostUSDNanos  int64   `bun:"estimated_cost_usd_nanos"`
	UnpricedCalls          int64   `bun:"unpriced_calls"`
}

// InsightsToolRow is the sum of one group's tool-call rollup rows.
type InsightsToolRow struct {
	Key             string  `bun:"key"`
	Calls           int64   `bun:"calls"`
	RetriedCalls    int64   `bun:"retried_calls"`
	ErrorCalls      int64   `bun:"error_calls"`
	CancelledCalls  int64   `bun:"cancelled_calls"`
	DurationMsTotal int64   `bun:"duration_ms_total"`
	DurationMsHist  []int64 `bun:"duration_ms_hist,array"`
}

const insightsModelSelect = `
	sum(calls)::bigint AS calls,
	sum(retried_calls)::bigint AS retried_calls,
	coalesce(sum(calls) FILTER (WHERE outcome = 'error'), 0)::bigint AS error_calls,
	coalesce(sum(calls) FILTER (WHERE outcome = 'cancelled'), 0)::bigint AS cancelled_calls,
	sum(input_tokens)::bigint AS input_tokens,
	sum(output_tokens)::bigint AS output_tokens,
	sum(cache_read_tokens)::bigint AS cache_read_tokens,
	sum(cache_write_tokens)::bigint AS cache_write_tokens,
	sum(latency_ms_total)::bigint AS latency_ms_total,
	insights_hist_sum(latency_ms_hist) AS latency_ms_hist,
	sum(ttft_ms_total)::bigint AS ttft_ms_total,
	insights_hist_sum(ttft_ms_hist) AS ttft_ms_hist,
	coalesce(sum(calls) FILTER (WHERE ` + insightsPaidByOrg + `), 0)::bigint AS org_funded_calls,
	coalesce(sum(calls) FILTER (WHERE ` + insightsPayerUnconfirmed + `), 0)::bigint AS payer_unconfirmed_calls,
	coalesce(sum(calls) FILTER (WHERE ` + insightsPersonalFunded + `), 0)::bigint AS personally_funded_calls,
	coalesce(sum(calls) FILTER (WHERE ` + insightsUnrecordedFunded + `), 0)::bigint AS unrecorded_funding_calls,
	coalesce(sum(reported_calls) FILTER (WHERE ` + insightsPaidByOrg + `), 0)::bigint AS reported_calls,
	coalesce(sum(reported_cost_usd_nanos) FILTER (WHERE ` + insightsPaidByOrg + `), 0)::bigint AS reported_cost_usd_nanos,
	coalesce(sum(estimated_calls) FILTER (WHERE ` + insightsPaidByOrg + `), 0)::bigint AS estimated_calls,
	coalesce(sum(estimated_cost_usd_nanos) FILTER (WHERE ` + insightsPaidByOrg + `), 0)::bigint AS estimated_cost_usd_nanos,
	coalesce(sum(unpriced_calls) FILTER (WHERE ` + insightsPaidByOrg + `), 0)::bigint AS unpriced_calls`

const insightsToolSelect = `
	sum(calls)::bigint AS calls,
	sum(retried_calls)::bigint AS retried_calls,
	coalesce(sum(calls) FILTER (WHERE outcome = 'error'), 0)::bigint AS error_calls,
	coalesce(sum(calls) FILTER (WHERE outcome = 'cancelled'), 0)::bigint AS cancelled_calls,
	sum(duration_ms_total)::bigint AS duration_ms_total,
	insights_hist_sum(duration_ms_hist) AS duration_ms_hist`

// insightsDays is a day range's bounds as SQL dates; from and to are UTC
// days, inclusive.
func insightsDays(from, to time.Time) (string, string) {
	return from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")
}

func insightsGroupSQL(table, sel, expr, owner string) string {
	return `SELECT ` + expr + ` AS key, ` + sel + `
		FROM ` + table + `
		WHERE ` + owner + ` AND day >= ?::date AND day <= ?::date
		GROUP BY 1 ORDER BY 1`
}

func insightsExpr(src InsightsSource, dim InsightsDim) (string, error) {
	expr, ok := insightsDimExpr[src][dim]
	if !ok {
		return "", fmt.Errorf("insights: %q is not a dimension of this source", dim)
	}
	return expr, nil
}

// ListInsightsTeamModelCalls sums orgID's TEAM model-call rollup over the UTC
// days from..to inclusive, one row per group of dim. The team table has no
// user column, so nothing it answers names a person.
func ListInsightsTeamModelCalls(ctx context.Context, db orm.Context, orgID string, from, to time.Time, dim InsightsDim) ([]InsightsModelRow, error) {
	if orgID == "" {
		return nil, errors.New("insights: org id is required")
	}
	expr, err := insightsExpr(InsightsModelCalls, dim)
	if err != nil {
		return nil, err
	}
	f, t := insightsDays(from, to)
	var rows []InsightsModelRow
	err = db.Bun().NewRaw(insightsGroupSQL(insightsModelTeam.name, insightsModelSelect, expr, `org_id = ?`), orgID, f, t).Scan(ctx, &rows)
	return rows, err
}

// ListInsightsTeamToolCalls is ListInsightsTeamModelCalls for tool calls.
func ListInsightsTeamToolCalls(ctx context.Context, db orm.Context, orgID string, from, to time.Time, dim InsightsDim) ([]InsightsToolRow, error) {
	if orgID == "" {
		return nil, errors.New("insights: org id is required")
	}
	expr, err := insightsExpr(InsightsToolCalls, dim)
	if err != nil {
		return nil, err
	}
	f, t := insightsDays(from, to)
	var rows []InsightsToolRow
	err = db.Bun().NewRaw(insightsGroupSQL(insightsToolTeam.name, insightsToolSelect, expr, `org_id = ?`), orgID, f, t).Scan(ctx, &rows)
	return rows, err
}

// ListInsightsMemberModelCalls sums userID's model-call rollup rows in orgID
// over the UTC days from..to inclusive, one row per group of dim: one
// member's own numbers, for their own view or an admin's audited drill-down.
// Cost columns count only calls the org provably paid for (paid_by_org), as
// on the team table.
func ListInsightsMemberModelCalls(ctx context.Context, db orm.Context, orgID, userID string, from, to time.Time, dim InsightsDim) ([]InsightsModelRow, error) {
	if orgID == "" || userID == "" {
		return nil, errors.New("insights: org id and user id are required")
	}
	expr, err := insightsExpr(InsightsModelCalls, dim)
	if err != nil {
		return nil, err
	}
	f, t := insightsDays(from, to)
	var rows []InsightsModelRow
	err = db.Bun().NewRaw(insightsGroupSQL(insightsModelMember.name, insightsModelSelect, expr, `user_id = ? AND org_id = ?`),
		userID, orgID, f, t).Scan(ctx, &rows)
	return rows, err
}

// ListInsightsMemberToolCalls is ListInsightsMemberModelCalls for tool calls.
func ListInsightsMemberToolCalls(ctx context.Context, db orm.Context, orgID, userID string, from, to time.Time, dim InsightsDim) ([]InsightsToolRow, error) {
	if orgID == "" || userID == "" {
		return nil, errors.New("insights: org id and user id are required")
	}
	expr, err := insightsExpr(InsightsToolCalls, dim)
	if err != nil {
		return nil, err
	}
	f, t := insightsDays(from, to)
	var rows []InsightsToolRow
	err = db.Bun().NewRaw(insightsGroupSQL(insightsToolMember.name, insightsToolSelect, expr, `user_id = ? AND org_id = ?`),
		userID, orgID, f, t).Scan(ctx, &rows)
	return rows, err
}

// InsightsContributor says that the member numbered Member contributed to the
// group Key. Member is an ordinal that means something only within one
// answer (the members of the answer, numbered 1..n in an order nothing
// outside the query sees): enough to count distinct contributors to a group,
// or to a union of groups, and nothing that names anyone.
type InsightsContributor struct {
	Key    string `bun:"key"`
	Member int64  `bun:"member"`
}

// ListInsightsContributors is, for each group of dim of src's calls in orgID
// over the UTC days from..to inclusive, the members who made at least one of
// them, as opaque ordinals (InsightsContributor). It reads the member table,
// whose rows mirror the team table's key plus user_id, so the groups are the
// team read's groups. A group the member table cannot name a contributor of
// (a deleted project's rows, merged into ” on the team table and gone from
// the member table) has fewer contributors here than it had, never more: the
// floor errs toward merging.
func ListInsightsContributors(ctx context.Context, db orm.Context, src InsightsSource, orgID string, from, to time.Time, dim InsightsDim) ([]InsightsContributor, error) {
	if orgID == "" {
		return nil, errors.New("insights: org id is required")
	}
	expr, err := insightsExpr(src, dim)
	if err != nil {
		return nil, err
	}
	table := insightsModelMember.name
	if src == InsightsToolCalls {
		table = insightsToolMember.name
	}
	f, t := insightsDays(from, to)
	var rows []InsightsContributor
	err = db.Bun().NewRaw(`SELECT key, dense_rank() OVER (ORDER BY user_id) AS member
		FROM (
			SELECT DISTINCT `+expr+` AS key, user_id
			FROM `+table+`
			WHERE org_id = ? AND day >= ?::date AND day <= ?::date
		) contributors
		ORDER BY key, member`, orgID, f, t).Scan(ctx, &rows)
	return rows, err
}
