package store

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// The insights rollups (dev-docs/design/agent-analytics.md §3.1, PR A4;
// migration 20261125002742_insights_rollups): what Agents › Insights reads,
// kept after the ledgers' rows are pruned. Per source, a team table an org's
// page reads, which has no user_id column, and a member table that adds it.
// A ledger row is folded into both in the transaction that writes it, or,
// for a row written before the rollups or by a previous release, by the
// backlog fold the retention job runs; insights_folded on the ledger row says
// which rows are counted, so each is counted exactly once.

// InsightsLatencyBuckets is the number of buckets of every *_hist column.
const InsightsLatencyBuckets = 48

// InsightsLatencyBucket is the histogram bucket a duration of ms falls in,
// the Go twin of the SQL insights_latency_bucket (a test holds them equal).
// Bucket 0 holds 0 ms; bucket k in 1..46 holds 2^((k-1)/2) <= ms < 2^(k/2);
// bucket 47 holds everything from 2^23 ms.
func InsightsLatencyBucket(ms int64) int {
	if ms < 1 {
		return 0
	}
	m := uint64(min(ms, 1<<24))
	return min(InsightsLatencyBuckets-1, bits.Len64(m*m))
}

// InsightsLatencyBucketBounds is the [lower, upper) range in ms of bucket k,
// for reading a percentile off a histogram. The last bucket's upper bound is
// +Inf.
func InsightsLatencyBucketBounds(k int) (lower, upper float64) {
	switch {
	case k <= 0:
		return 0, 1
	case k >= InsightsLatencyBuckets-1:
		return math.Pow(2, float64(InsightsLatencyBuckets-2)/2), math.Inf(1)
	}
	return math.Pow(2, float64(k-1)/2), math.Pow(2, float64(k)/2)
}

// The team tables count cost only for what an org paid for
// (insights_model_calls_daily_cost_is_org_funded).
const insightsOrgFunded = `funding IN ('org_key', 'org_seat', 'org_wallet')`

// insightsTable is one rollup table: its name, the columns that name whose
// totals a row holds (org_id, or user_id and org_id), and which ledger rows
// it counts.
type insightsTable struct {
	name   string
	owner  []string
	filter string
	// costScope narrows which calls' cost a model table counts.
	costScope string
	// team marks a team table: a call whose project is gone is counted under
	// no project (''), never under the deleted project's id.
	team bool
}

var (
	insightsModelTeam = insightsTable{name: "insights_model_calls_daily", owner: []string{"org_id"},
		filter: `org_id <> ''`, costScope: insightsOrgFunded, team: true}
	insightsModelMember = insightsTable{name: "insights_model_calls_member_daily", owner: []string{"user_id", "org_id"},
		filter: insightsMemberProject, costScope: "true"}
	insightsToolTeam = insightsTable{name: "insights_tool_calls_daily", owner: []string{"org_id"},
		filter: `org_id <> ''`, team: true}
	insightsToolMember = insightsTable{name: "insights_tool_calls_member_daily", owner: []string{"user_id", "org_id"},
		filter: insightsMemberProject}
)

// insightsMemberProject keeps a member row off a project that is gone: ?2 is
// the projects the folding transaction holds a key share on, so a project
// delete either waits for the fold and then deletes its rows, or committed
// first and the row is not written.
const insightsMemberProject = `(project_id = '' OR project_id = ANY(?2::text[]))`

// insightsSource is a ledger and how its rows fold into a rollup table.
type insightsSource struct {
	ledger string
	// dims are the key columns after the owner and day, as the subquery
	// names them (a NULL run-scope column is '').
	dims []string
	// columns are the subquery's select list beyond the key.
	columns string
	// measures are the rollup columns after the key, and what each sums.
	measures []insightsMeasure
}

type insightsMeasure struct {
	column string
	// agg is the aggregate over the source subquery; {cost_scope} is the
	// table's costScope.
	agg string
	// hist marks a histogram, which merges with insights_hist_add.
	hist bool
}

var insightsRunScope = []string{"project_id", "workflow_name", "node_id", "preset"}

var llmInsights = insightsSource{
	ledger: "llm_call_usage",
	dims:   append(slices.Clone(insightsRunScope), "provider", "model", "auth_mode", "funding", "purpose", "outcome", "error_class"),
	columns: `provider, model, auth_mode, funding, purpose, outcome, error_class, attempt,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, latency_ms, ttft_ms, cost_basis, cost_usd_nanos`,
	measures: []insightsMeasure{
		{column: "calls", agg: `count(*)`},
		{column: "retried_calls", agg: `count(*) FILTER (WHERE attempt > 1)`},
		{column: "input_tokens", agg: `sum(input_tokens)::bigint`},
		{column: "output_tokens", agg: `sum(output_tokens)::bigint`},
		{column: "cache_read_tokens", agg: `sum(cache_read_tokens)::bigint`},
		{column: "cache_write_tokens", agg: `sum(cache_write_tokens)::bigint`},
		{column: "latency_ms_total", agg: `sum(latency_ms)::bigint`},
		{column: "latency_ms_hist", agg: `insights_hist(latency_ms)`, hist: true},
		{column: "ttft_calls", agg: `count(ttft_ms)`},
		{column: "ttft_ms_total", agg: `coalesce(sum(ttft_ms), 0)::bigint`},
		{column: "ttft_ms_hist", agg: `insights_hist(ttft_ms)`, hist: true},
		{column: "reported_calls", agg: `count(*) FILTER (WHERE cost_basis = 'reported' AND {cost_scope})`},
		{column: "reported_cost_usd_nanos", agg: `coalesce(sum(cost_usd_nanos) FILTER (WHERE cost_basis = 'reported' AND {cost_scope}), 0)::bigint`},
		{column: "estimated_calls", agg: `count(*) FILTER (WHERE cost_basis = 'estimated' AND {cost_scope})`},
		{column: "estimated_cost_usd_nanos", agg: `coalesce(sum(cost_usd_nanos) FILTER (WHERE cost_basis = 'estimated' AND {cost_scope}), 0)::bigint`},
		{column: "unpriced_calls", agg: `count(*) FILTER (WHERE cost_basis = 'unpriced')`},
	},
}

var toolInsights = insightsSource{
	ledger:  "tool_call_usage",
	dims:    append(slices.Clone(insightsRunScope), "tool_name", "tool_kind", "location", "outcome", "error_class"),
	columns: `tool_name, tool_kind, location, outcome, error_class, attempt, duration_ms`,
	measures: []insightsMeasure{
		{column: "calls", agg: `count(*)`},
		{column: "retried_calls", agg: `count(*) FILTER (WHERE attempt > 1)`},
		{column: "duration_ms_total", agg: `sum(duration_ms)::bigint`},
		{column: "duration_ms_hist", agg: `insights_hist(duration_ms)`, hist: true},
	},
}

// foldSQL is the statement that adds the ledger rows named by ?0 (ids) and
// ?1 (attempts) into t. It groups them by t's key and takes the rows in key
// order, so two folds that meet on the same rollup rows lock them in the same
// order and cannot deadlock.
func (s insightsSource) foldSQL(t insightsTable) string {
	key := append(slices.Clone(t.owner), "day")
	key = append(key, s.dims...)
	keyList := strings.Join(key, ", ")

	cols := make([]string, 0, len(s.measures))
	aggs := make([]string, 0, len(s.measures))
	for _, m := range s.measures {
		cols = append(cols, m.column)
		aggs = append(aggs, strings.ReplaceAll(m.agg, "{cost_scope}", t.costScope))
	}

	// A member row of a gone project is not written (t.filter); a team row
	// is counted under no project.
	project := `coalesce(project_id, '')`
	if t.team {
		project = `CASE WHEN project_id = ANY(?2::text[]) THEN project_id ELSE '' END`
	}
	return `INSERT INTO ` + t.name + ` AS d (` + keyList + `, ` + strings.Join(cols, ", ") + `, updated_at)
		SELECT ` + keyList + `, ` + strings.Join(aggs, ", ") + `, now()
		FROM (
			SELECT user_id, org_id, (created_at AT TIME ZONE 'UTC')::date AS day,
				` + project + ` AS project_id, coalesce(workflow_name, '') AS workflow_name,
				coalesce(node_id, '') AS node_id, coalesce(preset, '') AS preset, ` + s.columns + `
			FROM ` + s.ledger + `
			WHERE (id, attempt) IN (SELECT * FROM unnest(?0::text[], ?1::integer[]))
		) src
		WHERE ` + t.filter + `
		GROUP BY ` + keyList + `
		ORDER BY ` + keyList + `
		ON CONFLICT (` + keyList + `) DO UPDATE SET ` + s.conflictSets()
}

// fold adds the ledger rows (ids[i], attempts[i]) to the source's team and
// member tables, team first. projects are the ids of the projects the caller
// holds a key share on: a member row naming any other project is not written.
// The caller's ctx must be the transaction that wrote or claimed the rows.
func (s insightsSource) fold(ctx context.Context, db orm.Context, ids []string, attempts []int32, projects []string, team, member insightsTable) error {
	if len(ids) == 0 {
		return nil
	}
	for _, t := range []insightsTable{team, member} {
		if _, err := db.Bun().NewRaw(s.foldSQL(t), pgdialect.Array(ids), pgdialect.Array(attempts), pgdialect.Array(projects)).Exec(ctx); err != nil {
			return fmt.Errorf("fold %s into %s: %w", s.ledger, t.name, err)
		}
	}
	return nil
}

// lockInsightsProjects takes a key share on each of the projects that exists
// and returns those ids: a project delete then waits for the caller's
// transaction, and deletes the member rows it wrote.
func lockInsightsProjects(ctx context.Context, db orm.Context, projectIDs []string) ([]string, error) {
	ids := make([]string, 0, len(projectIDs))
	for _, id := range projectIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []string{}, nil
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	locked := []string{}
	if err := db.Bun().NewRaw(`SELECT id FROM projects WHERE id = ANY(?0::text[]) ORDER BY id FOR KEY SHARE`,
		pgdialect.Array(ids)).Scan(ctx, &locked); err != nil {
		return nil, fmt.Errorf("lock projects for insights: %w", err)
	}
	return locked, nil
}

// foldOne folds the ledger row a write just inserted.
func (s insightsSource) foldOne(ctx context.Context, db orm.Context, id string, attempt int32, projectID string, team, member insightsTable) error {
	projects, err := lockInsightsProjects(ctx, db, []string{projectID})
	if err != nil {
		return err
	}
	return s.fold(ctx, db, []string{id}, []int32{attempt}, projects, team, member)
}

// foldBacklog folds up to batch of the oldest ledger rows not yet counted and
// returns how many it folded. A row is claimed (FOR UPDATE SKIP LOCKED, so
// replicas never share one) and marked folded in the transaction that adds
// it, so a run that fails adds nothing and a second run finds nothing to add.
// The caller's ctx must be a transaction.
//
// It takes each owning user's usage lock (shared) before claiming their rows,
// as a live write does: an account purge holds that lock exclusively while it
// deletes the user's rows, so it either waits for this fold and then deletes
// what it wrote, or committed first and left no rows to fold.
func (s insightsSource) foldBacklog(ctx context.Context, db orm.Context, batch int, team, member insightsTable) (int64, error) {
	if batch <= 0 {
		return 0, fmt.Errorf("fold %s into insights: batch must be positive", s.ledger)
	}
	users := []string{}
	if err := db.Bun().NewRaw(`SELECT DISTINCT user_id FROM (
			SELECT user_id FROM `+s.ledger+` WHERE NOT insights_folded ORDER BY created_at LIMIT ?0) c`,
		orm.Arg(batch)).Scan(ctx, &users); err != nil {
		return 0, fmt.Errorf("list %s users to fold: %w", s.ledger, err)
	}
	if len(users) == 0 {
		return 0, nil
	}
	keys := make([]string, len(users))
	for i, u := range users {
		keys[i] = LLMUsageUserLockKey(u)
	}
	// Shared locks never wait on one another, so their order cannot deadlock.
	if _, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock_shared(hashtextextended(k, 0)) FROM unnest(?0::text[]) AS k`,
		pgdialect.Array(keys)).Exec(ctx); err != nil {
		return 0, fmt.Errorf("lock %s users to fold: %w", s.ledger, err)
	}

	var claimed []struct {
		ID        string  `bun:"id"`
		Attempt   int32   `bun:"attempt"`
		ProjectID *string `bun:"project_id"`
	}
	if err := db.Bun().NewRaw(`UPDATE `+s.ledger+` SET insights_folded = true
		WHERE (id, attempt) IN (
			SELECT id, attempt FROM `+s.ledger+`
			WHERE NOT insights_folded AND user_id = ANY(?0::text[])
			ORDER BY created_at LIMIT ?1 FOR UPDATE SKIP LOCKED)
		RETURNING id, attempt, project_id`, pgdialect.Array(users), orm.Arg(batch)).Scan(ctx, &claimed); err != nil {
		return 0, fmt.Errorf("claim %s rows to fold: %w", s.ledger, err)
	}
	ids := make([]string, len(claimed))
	attempts := make([]int32, len(claimed))
	projectIDs := make([]string, 0, len(claimed))
	for i, c := range claimed {
		ids[i], attempts[i] = c.ID, c.Attempt
		if c.ProjectID != nil {
			projectIDs = append(projectIDs, *c.ProjectID)
		}
	}
	projects, err := lockInsightsProjects(ctx, db, projectIDs)
	if err != nil {
		return 0, err
	}
	if err := s.fold(ctx, db, ids, attempts, projects, team, member); err != nil {
		return 0, err
	}
	return int64(len(claimed)), nil
}

// conflictSets is the ON CONFLICT assignments that add EXCLUDED's counts to
// the row's.
func (s insightsSource) conflictSets() string {
	sets := make([]string, 0, len(s.measures)+1)
	for _, m := range s.measures {
		if m.hist {
			sets = append(sets, fmt.Sprintf("%[1]s = insights_hist_add(d.%[1]s, EXCLUDED.%[1]s)", m.column))
		} else {
			sets = append(sets, fmt.Sprintf("%[1]s = d.%[1]s + EXCLUDED.%[1]s", m.column))
		}
	}
	return strings.Join(append(sets, "updated_at = now()"), ", ")
}

// mergeIntoNoProjectSQL moves the team rows of t that gone selects (a WHERE
// clause over t) into the no-project bucket: each is deleted and its counts
// are added to the row of the same org, day and other keys with project_id
// ”. Only project_id changes, so rows of one project map to distinct
// buckets; rows of several projects (the orphan sweep) can share one, so
// they are summed per bucket first, which also keeps the INSERT from touching
// a row twice. The deleted rows all name a project, so none of them is a
// bucket the INSERT adds to.
func (s insightsSource) mergeIntoNoProjectSQL(t insightsTable, gone string) string {
	key := append(slices.Clone(t.owner), "day")
	key = append(key, s.dims...)
	sel := make([]string, len(key))
	groups := make([]string, len(key))
	for i, c := range key {
		sel[i], groups[i] = c, strconv.Itoa(i+1)
		if c == "project_id" {
			sel[i] = `''`
		}
	}
	cols := make([]string, len(s.measures))
	sums := make([]string, len(s.measures))
	for i, m := range s.measures {
		cols[i] = m.column
		sums[i] = "sum(" + m.column + ")::bigint"
		if m.hist {
			sums[i] = "insights_hist_sum(" + m.column + ")"
		}
	}
	keyList := strings.Join(key, ", ")
	return `WITH gone AS (DELETE FROM ` + t.name + ` WHERE ` + gone + ` RETURNING ` + keyList + `, ` + strings.Join(cols, ", ") + `)
		INSERT INTO ` + t.name + ` AS d (` + keyList + `, ` + strings.Join(cols, ", ") + `, updated_at)
		SELECT ` + strings.Join(sel, ", ") + `, ` + strings.Join(sums, ", ") + `, now()
		FROM gone
		GROUP BY ` + strings.Join(groups, ", ") + `
		ORDER BY ` + strings.Join(groups, ", ") + `
		ON CONFLICT (` + keyList + `) DO UPDATE SET ` + s.conflictSets()
}

// insightsTeamTables are the team tables, with the ledger each counts.
var insightsTeamTables = []struct {
	src insightsSource
	t   insightsTable
}{{llmInsights, insightsModelTeam}, {toolInsights, insightsToolTeam}}

// mergeInsightsTeamRowsOfProject moves the team rows that name the project
// into the no-project bucket, so the org's totals stay whole and the deleted
// project's id is gone. Project delete runs it in the transaction that
// deletes the project row, after the delete, for the reason
// deleteInsightsMemberRowsOfProject gives.
func mergeInsightsTeamRowsOfProject(ctx context.Context, db orm.Context, projectID string) error {
	for _, team := range insightsTeamTables {
		if _, err := db.Bun().NewRaw(team.src.mergeIntoNoProjectSQL(team.t, `project_id = ?0`), projectID).Exec(ctx); err != nil {
			return fmt.Errorf("merge %s of the project: %w", team.t.name, err)
		}
	}
	return nil
}

// insightsTeamOrphanMerge is the orphan sweep's step for a team table: up to
// ?0 rows naming a project that is gone (one an account purge deleted) move
// into the no-project bucket.
func insightsTeamOrphanMerge(src insightsSource, t insightsTable) string {
	return src.mergeIntoNoProjectSQL(t, `ctid IN (
			SELECT m.ctid FROM `+t.name+` m
			WHERE m.project_id <> '' AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = m.project_id)
			LIMIT ?0)`)
}

// insightsMemberTables are the rollup tables that name a person: account
// purge and project delete remove their rows.
var insightsMemberTables = []string{insightsModelMember.name, insightsToolMember.name}

// deleteInsightsMemberRowsOfProject removes the member rows that name the
// project. Project delete runs it in the transaction that deletes the project
// row, after the delete: a fold holding a key share on the project was waited
// for, and its rows are seen here.
func deleteInsightsMemberRowsOfProject(ctx context.Context, db orm.Context, projectID string) error {
	for _, table := range insightsMemberTables {
		if _, err := db.Bun().NewRaw(`DELETE FROM `+table+` WHERE project_id = ?0`, projectID).Exec(ctx); err != nil {
			return fmt.Errorf("delete %s of the project: %w", table, err)
		}
	}
	return nil
}
