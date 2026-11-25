// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/llmusage/llmusagetest"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The insights rollups (agent-analytics.md §3.1, PR A4): an org's team
// tables with no user_id, the member tables with it, written with each ledger
// row and folded from the rows that predate them.

// insightsAt is fixture data, not a clock: the first day the fixtures use.
var insightsAt = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

type insightsFixture struct {
	t      *testing.T
	env    *storetest.Env
	ledger llmusage.Service
	ctx    context.Context
}

func newInsightsFixture(t *testing.T, projects ...string) *insightsFixture {
	t.Helper()
	env := storetest.New(t)
	f := &insightsFixture{t: t, env: env, ledger: llmusagetest.New(t, env.Repo), ctx: context.Background()}
	for _, p := range projects {
		f.exec(`INSERT INTO projects (id, name, path, user_id, created_at, updated_at, last_active)
			VALUES ($1, $1, '/srv/' || $1, $2, $3, $3, $3)`, p, storetest.SeedUserID, insightsAt)
	}
	return f
}

func (f *insightsFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.env.SQL.Exec(query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func (f *insightsFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.env.SQL.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (f *insightsFixture) model(rec store.LLMCallUsageRecord) {
	f.t.Helper()
	if _, err := f.ledger.RecordLLMCallUsage(f.ctx, rec); err != nil {
		f.t.Fatalf("RecordLLMCallUsage %s/%d: %v", rec.ID, rec.Attempt, err)
	}
}

func (f *insightsFixture) tool(rec store.ToolCallUsageRecord) {
	f.t.Helper()
	if _, err := f.ledger.RecordToolCallUsage(f.ctx, rec); err != nil {
		f.t.Fatalf("RecordToolCallUsage %s/%d: %v", rec.ID, rec.Attempt, err)
	}
}

func (f *insightsFixture) fold() int64 {
	f.t.Helper()
	n, err := f.ledger.FoldInsights(f.ctx)
	if err != nil {
		f.t.Fatalf("FoldInsights: %v", err)
	}
	return n
}

// forgetRollups puts the database where it was before the rollups existed:
// every ledger row unfolded and the rollups empty.
func (f *insightsFixture) forgetRollups() {
	f.t.Helper()
	f.exec(`UPDATE llm_call_usage SET insights_folded = false`)
	f.exec(`UPDATE tool_call_usage SET insights_folded = false`)
	for _, table := range insightsTables {
		f.exec(`DELETE FROM ` + table.name)
	}
}

// insightsTableShape is a rollup table as the tests read it: its key columns and
// its counts, histograms last.
type insightsTableShape struct {
	name     string
	key      []string
	measures []string
}

var (
	modelRunKey   = []string{"day", "project_id", "workflow_name", "node_id", "preset", "provider", "model", "auth_mode", "funding", "paid_by_org", "purpose", "outcome", "error_class"}
	modelMeasures = []string{"calls", "retried_calls", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"latency_ms_total", "ttft_calls", "ttft_ms_total", "reported_calls", "reported_cost_usd_nanos", "estimated_calls",
		"estimated_cost_usd_nanos", "unpriced_calls", "latency_ms_hist", "ttft_ms_hist"}
	toolRunKey     = []string{"day", "project_id", "workflow_name", "node_id", "preset", "tool_name", "tool_kind", "location", "outcome", "error_class"}
	toolMeasures   = []string{"calls", "retried_calls", "duration_ms_total", "duration_ms_hist"}
	modelTeam      = insightsTableShape{"insights_model_calls_daily", append([]string{"org_id"}, modelRunKey...), modelMeasures}
	modelMember    = insightsTableShape{"insights_model_calls_member_daily", append([]string{"user_id", "org_id"}, modelRunKey...), modelMeasures}
	toolTeam       = insightsTableShape{"insights_tool_calls_daily", append([]string{"org_id"}, toolRunKey...), toolMeasures}
	toolMember     = insightsTableShape{"insights_tool_calls_member_daily", append([]string{"user_id", "org_id"}, toolRunKey...), toolMeasures}
	insightsTables = []insightsTableShape{modelTeam, modelMember, toolTeam, toolMember}
)

// read is the table as key → counts, each a "|"-joined string.
func (f *insightsFixture) read(table insightsTableShape) map[string]string {
	f.t.Helper()
	keyCols := make([]string, len(table.key))
	for i, c := range table.key {
		if c == "day" {
			c = "to_char(day, 'YYYY-MM-DD')"
		}
		keyCols[i] = c
	}
	valCols := make([]string, len(table.measures))
	for i, c := range table.measures {
		if strings.HasSuffix(c, "_hist") {
			c = "array_to_string(" + c + ", ',')"
		}
		valCols[i] = c
	}
	rows, err := f.env.SQL.Query(`SELECT concat_ws('|', ` + strings.Join(keyCols, ", ") + `), concat_ws('|', ` +
		strings.Join(valCols, ", ") + `) FROM ` + table.name)
	if err != nil {
		f.t.Fatalf("read %s: %v", table.name, err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			f.t.Fatalf("read %s: %v", table.name, err)
		}
		got[k] = v
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("read %s: %v", table.name, err)
	}
	return got
}

func (f *insightsFixture) readAll() map[string]map[string]string {
	all := map[string]map[string]string{}
	for _, table := range insightsTables {
		all[table.name] = f.read(table)
	}
	return all
}

// insightsSum is a direct sum of fixture rows: what a rollup row must hold.
type insightsSum struct {
	n          [14]int64 // the non-histogram measures, in modelMeasures' order
	hist, ttft [store.InsightsLatencyBuckets]int64
}

// format is the sum as read() prints a row: plain counts, then histograms.
func (s *insightsSum) format(measures int) string {
	model := measures == len(modelMeasures)
	plain := len(toolMeasures) - 1
	if model {
		plain = len(modelMeasures) - 2
	}
	parts := make([]string, 0, measures)
	for _, v := range s.n[:plain] {
		parts = append(parts, strconv.FormatInt(v, 10))
	}
	parts = append(parts, joinInts(s.hist[:]))
	if model {
		parts = append(parts, joinInts(s.ttft[:]))
	}
	return strings.Join(parts, "|")
}

func joinInts(v []int64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.FormatInt(x, 10)
	}
	return strings.Join(s, ",")
}

// paidByTheOrg is whether a team row of org counts the call's cost: an org
// paid for it, and that org is org itself, not merely the work's org.
func paidByTheOrg(r store.LLMCallUsageRecord, org string) bool {
	orgFunded := r.Funding == "org_key" || r.Funding == "org_seat" || r.Funding == "org_wallet"
	return orgFunded && r.FundingOrgID != "" && r.FundingOrgID == org
}

// expectModels sums the model calls the way the rollups must: one row per
// (id, attempt), the first write winning; the team tables only for an org,
// with cost only for calls that org itself paid for.
func expectModels(recs []store.LLMCallUsageRecord) (team, member map[string]string) {
	teamSums, memberSums := map[string]*insightsSum{}, map[string]*insightsSum{}
	seen := map[string]bool{}
	for _, r := range recs {
		id := r.ID + "/" + strconv.Itoa(int(r.Attempt))
		if seen[id] {
			continue
		}
		seen[id] = true
		paid := paidByTheOrg(r, r.OrgID)
		run := strings.Join([]string{r.CreatedAt.UTC().Format("2006-01-02"), r.ProjectID, r.WorkflowName, r.NodeID, r.Preset,
			r.Provider, r.Model, r.AuthMode, r.Funding, map[bool]string{true: "t", false: "f"}[paid], r.Purpose, r.Outcome, r.ErrorClass}, "|")
		add := func(sums map[string]*insightsSum, key string, costed bool) {
			s := sums[key]
			if s == nil {
				s = &insightsSum{}
				sums[key] = s
			}
			s.n[0]++
			if r.Attempt > 1 {
				s.n[1]++
			}
			s.n[2] += r.InputTokens
			s.n[3] += r.OutputTokens
			s.n[4] += r.CacheReadTokens
			s.n[5] += r.CacheWriteTokens
			s.n[6] += r.LatencyMs
			s.hist[store.InsightsLatencyBucket(r.LatencyMs)]++
			if r.TTFTMs != nil {
				s.n[7]++
				s.n[8] += *r.TTFTMs
				s.ttft[store.InsightsLatencyBucket(*r.TTFTMs)]++
			}
			switch {
			case r.CostBasis == store.LLMCostBasisReported && costed:
				s.n[9]++
				s.n[10] += *r.CostUSDNanos
			case r.CostBasis == store.LLMCostBasisEstimated && costed:
				s.n[11]++
				s.n[12] += *r.CostUSDNanos
			case r.CostBasis == store.LLMCostBasisUnpriced:
				s.n[13]++
			}
		}
		if r.OrgID != "" {
			add(teamSums, r.OrgID+"|"+run, paid)
		}
		add(memberSums, r.UserID+"|"+r.OrgID+"|"+run, true)
	}
	return formatSums(teamSums, len(modelMeasures)), formatSums(memberSums, len(modelMeasures))
}

func expectTools(recs []store.ToolCallUsageRecord) (team, member map[string]string) {
	teamSums, memberSums := map[string]*insightsSum{}, map[string]*insightsSum{}
	seen := map[string]bool{}
	for _, r := range recs {
		id := r.ID + "/" + strconv.Itoa(int(r.Attempt))
		if seen[id] {
			continue
		}
		seen[id] = true
		run := strings.Join([]string{r.CreatedAt.UTC().Format("2006-01-02"), r.ProjectID, r.WorkflowName, r.NodeID, r.Preset,
			r.ToolName, r.ToolKind, r.Location, r.Outcome, r.ErrorClass}, "|")
		add := func(sums map[string]*insightsSum, key string) {
			s := sums[key]
			if s == nil {
				s = &insightsSum{}
				sums[key] = s
			}
			s.n[0]++
			if r.Attempt > 1 {
				s.n[1]++
			}
			s.n[2] += r.DurationMs
			s.hist[store.InsightsLatencyBucket(r.DurationMs)]++
		}
		if r.OrgID != "" {
			add(teamSums, r.OrgID+"|"+run)
		}
		add(memberSums, r.UserID+"|"+r.OrgID+"|"+run)
	}
	return formatSums(teamSums, len(toolMeasures)), formatSums(memberSums, len(toolMeasures))
}

func formatSums(sums map[string]*insightsSum, measures int) map[string]string {
	out := make(map[string]string, len(sums))
	for k, s := range sums {
		out[k] = s.format(measures)
	}
	return out
}

// Durations that straddle bucket edges, both ends of the scale included.
var insightsDurations = []int64{0, 1, 2, 3, 4, 5, 181, 182, 255, 256, 1000, 1448, 1449, 65535, 65536,
	1 << 23, 1<<23 + 1, 1 << 24, 1<<24 + 7, 1_000_000_000}

func pick[T any](r *rand.Rand, from ...T) T { return from[r.IntN(len(from))] }

// randomModelCalls is n model calls, with retries (a later attempt of an
// earlier id) and duplicate deliveries (a repeat of an earlier record).
func randomModelCalls(r *rand.Rand, prefix string, n int) []store.LLMCallUsageRecord {
	var recs []store.LLMCallUsageRecord
	for i := 0; i < n; i++ {
		if len(recs) > 0 && r.IntN(10) == 0 {
			recs = append(recs, recs[r.IntN(len(recs))])
			continue
		}
		rec := store.LLMCallUsageRecord{
			ID: fmt.Sprintf("%s-m-%d", prefix, i), Attempt: 1,
			UserID: pick(r, "alice", "bob", "carol"), OrgID: pick(r, "", "org-1", "org-2"),
			ChatID: pick(r, "chat-1", "chat-2"), Provider: pick(r, "reliant", "anthropic"), Model: pick(r, "m-small", "m-large"),
			AuthMode: pick(r, "managed", "byok", "subscription", "local"), Purpose: pick(r, "chat", "compaction", "title"),
			Funding: pick(r, "", "org_key", "org_seat", "org_wallet", "personal", "local"),
			// The payer: unknown, the work's org, or another org.
			FundingOrgID: pick(r, "", "org-1", "org-2"),
			ProjectID:    pick(r, "", "proj-a", "proj-b"), WorkflowName: pick(r, "", "agent", "get-it-right"),
			NodeID: pick(r, "", "call_llm", "review"), Preset: pick(r, "", "researcher"),
			InputTokens: r.Int64N(5000), OutputTokens: r.Int64N(2000), CacheReadTokens: r.Int64N(3000), CacheWriteTokens: r.Int64N(100),
			LatencyMs: pick(r, insightsDurations...),
			CreatedAt: insightsAt.Add(time.Duration(r.IntN(2*24*3600)) * time.Second),
		}
		if r.IntN(3) > 0 {
			ttft := pick(r, insightsDurations...)
			rec.TTFTMs = &ttft
		}
		switch r.IntN(4) {
		case 0:
			rec.Outcome = store.LLMOutcomeSuccess
		case 1:
			rec.Outcome = store.LLMOutcomeCancelled
		default:
			rec.Outcome, rec.ErrorClass = store.LLMOutcomeError, pick(r, "rate_limit", "timeout", "network")
		}
		switch basis := pick(r, "", store.LLMCostBasisReported, store.LLMCostBasisEstimated, store.LLMCostBasisUnpriced); basis {
		case store.LLMCostBasisReported, store.LLMCostBasisEstimated:
			cost := r.Int64N(50_000_000)
			rec.CostUSDNanos, rec.CostBasis = &cost, basis
		case store.LLMCostBasisUnpriced:
			rec.CostBasis = basis
		}
		if len(recs) > 0 && r.IntN(5) == 0 {
			// A retry: the next attempt of an earlier call.
			prev := recs[r.IntN(len(recs))]
			rec.ID, rec.Attempt, rec.UserID, rec.OrgID = prev.ID, prev.Attempt+1, prev.UserID, prev.OrgID
		}
		recs = append(recs, rec)
	}
	return recs
}

func randomToolCalls(r *rand.Rand, prefix string, n int) []store.ToolCallUsageRecord {
	var recs []store.ToolCallUsageRecord
	for i := 0; i < n; i++ {
		if len(recs) > 0 && r.IntN(10) == 0 {
			recs = append(recs, recs[r.IntN(len(recs))])
			continue
		}
		rec := store.ToolCallUsageRecord{
			ID: fmt.Sprintf("%s-t-%d", prefix, i), Attempt: 1,
			UserID: pick(r, "alice", "bob", "carol"), OrgID: pick(r, "", "org-1", "org-2"), ChatID: pick(r, "chat-1", "chat-2"),
			ProjectID: pick(r, "", "proj-a", "proj-b"), WorkflowName: pick(r, "", "agent"), NodeID: pick(r, "", "tools"),
			Preset: pick(r, "", "researcher"), Location: pick(r, "daemon", "local"),
			DurationMs: pick(r, insightsDurations...),
			CreatedAt:  insightsAt.Add(time.Duration(r.IntN(2*24*3600)) * time.Second),
		}
		if r.IntN(3) == 0 {
			rec.ToolKind = pick(r, store.ToolKindMCP, store.ToolKindUnknown)
		} else {
			rec.ToolKind, rec.ToolName = store.ToolKindBuiltin, pick(r, "shell", "read_file")
		}
		if r.IntN(3) == 0 {
			rec.Outcome, rec.ErrorClass = store.ToolOutcomeError, pick(r, store.ToolErrorTimeout, store.ToolErrorTool)
		} else {
			rec.Outcome = pick(r, store.ToolOutcomeSuccess, store.ToolOutcomeCancelled)
		}
		if len(recs) > 0 && r.IntN(5) == 0 {
			prev := recs[r.IntN(len(recs))]
			rec.ID, rec.Attempt, rec.UserID, rec.OrgID = prev.ID, prev.Attempt+1, prev.UserID, prev.OrgID
		}
		recs = append(recs, rec)
	}
	return recs
}

func assertRollup(t *testing.T, table string, want, got map[string]string) {
	t.Helper()
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if got[k] != want[k] {
			t.Errorf("%s[%s]\n got %q\nwant %q", table, k, got[k], want[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[k]; !ok {
			t.Errorf("%s[%s] = %q, a row no fixture row sums to", table, k, got[k])
		}
	}
}

// TestInsightsRollupEqualsDirectSum is the property the rollups promise: for
// any set of calls, each table holds exactly the direct sum of its rows,
// whether a row was folded with its write or by the backfill of rows that
// predate the rollups, with retries and duplicate deliveries among them.
func TestInsightsRollupEqualsDirectSum(t *testing.T) {
	t.Parallel()
	for _, seed := range []uint64{1, 2, 3} {
		t.Run("seed="+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			f := newInsightsFixture(t, "proj-a", "proj-b")
			r := rand.New(rand.NewPCG(seed, 0xA4))
			models := randomModelCalls(r, "s"+strconv.FormatUint(seed, 10), 150)
			tools := randomToolCalls(r, "s"+strconv.FormatUint(seed, 10), 150)

			// The first half predates the rollups; the second half is written
			// after them, some of it retrying or repeating the first half.
			for _, rec := range models[:len(models)/2] {
				f.model(rec)
			}
			for _, rec := range tools[:len(tools)/2] {
				f.tool(rec)
			}
			f.forgetRollups()
			for _, rec := range models[len(models)/2:] {
				f.model(rec)
			}
			for _, rec := range tools[len(tools)/2:] {
				f.tool(rec)
			}
			if n := f.fold(); n == 0 {
				t.Fatal("the backfill folded nothing; the fixture's first half was unfolded")
			}

			got := f.readAll()
			team, member := expectModels(models)
			assertRollup(t, modelTeam.name, team, got[modelTeam.name])
			assertRollup(t, modelMember.name, member, got[modelMember.name])
			team, member = expectTools(tools)
			assertRollup(t, toolTeam.name, team, got[toolTeam.name])
			assertRollup(t, toolMember.name, member, got[toolMember.name])
		})
	}
}

// TestInsightsRetriedAttemptCountedOnce: a retried call is two attempts, each
// a real request, so both are counted and the second is a retry; the
// duplicate delivery of an attempt, before or after the backfill runs, adds
// nothing.
func TestInsightsRetriedAttemptCountedOnce(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t, "proj-a")
	call := store.LLMCallUsageRecord{
		ID: "wf/run/act/chat/0", Attempt: 1, UserID: "alice", OrgID: "org-1", ChatID: "chat-1", ProjectID: "proj-a",
		Provider: "reliant", Model: "m", AuthMode: "managed", Purpose: "chat", Funding: "org_wallet",
		Outcome: store.LLMOutcomeError, ErrorClass: "timeout", LatencyMs: 900, CreatedAt: insightsAt,
	}
	retry := call
	retry.Attempt, retry.Outcome, retry.ErrorClass, retry.LatencyMs = 2, store.LLMOutcomeError, "timeout", 1100
	tool := store.ToolCallUsageRecord{
		ID: "wf/run/act/tc-1", Attempt: 1, UserID: "alice", OrgID: "org-1", ChatID: "chat-1", ProjectID: "proj-a",
		ToolName: "shell", ToolKind: store.ToolKindBuiltin, Location: "daemon",
		Outcome: store.ToolOutcomeError, ErrorClass: store.ToolErrorInterrupted, CreatedAt: insightsAt,
	}
	toolRetry := tool
	toolRetry.Attempt = 2

	f.model(call)
	f.model(retry)
	f.model(retry) // a duplicate delivery of attempt 2
	f.tool(tool)
	f.tool(toolRetry)
	f.tool(toolRetry)
	if n := f.fold(); n != 0 {
		t.Fatalf("the backfill folded %d rows written with the rollups, want 0", n)
	}
	f.model(retry)

	for _, table := range []string{modelTeam.name, modelMember.name, toolTeam.name, toolMember.name} {
		var calls, retried int
		if err := f.env.SQL.QueryRow(`SELECT sum(calls), sum(retried_calls) FROM `+table).Scan(&calls, &retried); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if calls != 2 || retried != 1 {
			t.Errorf("%s: calls=%d retried_calls=%d, want 2 attempts of which 1 retry (1 logical call)", table, calls, retried)
		}
	}
}

// TestInsightsTeamTablesHaveNoUserID is the privacy rule in the schema
// (§3.6): the team tables, which the team page reads, cannot name a person,
// because no column of theirs can hold one. The member tables can, and are
// the only ones that do.
func TestInsightsTeamTablesHaveNoUserID(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	for _, table := range insightsTables {
		var cols []string
		rows, err := f.env.SQL.Query(`SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1`, table.name)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		rows.Close()
		if len(cols) == 0 {
			t.Fatalf("%s: no such table", table.name)
		}
		member := strings.Contains(table.name, "_member_")
		hasUser := slices.ContainsFunc(cols, func(c string) bool {
			return strings.Contains(c, "user") || strings.Contains(c, "account") || strings.Contains(c, "chat")
		})
		if !member && hasUser {
			t.Errorf("%s is a team table and has a column naming a person or their chat: %v", table.name, cols)
		}
		if member && !slices.Contains(cols, "user_id") {
			t.Errorf("%s is a member table and has no user_id: %v", table.name, cols)
		}
	}
	// And no call made outside an org reaches a team table.
	f.model(store.LLMCallUsageRecord{ID: "personal", Attempt: 1, UserID: "alice", ChatID: "c", Provider: "p", Model: "m",
		AuthMode: "byok", Purpose: "chat", Outcome: store.LLMOutcomeSuccess, CreatedAt: insightsAt})
	if n := f.count(`SELECT count(*) FROM insights_model_calls_daily`); n != 0 {
		t.Errorf("a call outside an org wrote %d team rows, want 0", n)
	}
	if n := f.count(`SELECT count(*) FROM insights_model_calls_member_daily WHERE user_id = 'alice'`); n != 1 {
		t.Errorf("a call outside an org wrote %d member rows, want 1", n)
	}
}

// TestInsightsBackfillIsIdempotent: folding the rows that predate the rollups
// counts each once, however many times and from however many replicas it
// runs, and a row written with the rollups is never folded again.
func TestInsightsBackfillIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t, "proj-a", "proj-b")
	r := rand.New(rand.NewPCG(7, 0xA4))
	for _, rec := range randomModelCalls(r, "old", 60) {
		f.model(rec)
	}
	for _, rec := range randomToolCalls(r, "old", 60) {
		f.tool(rec)
	}
	want := f.readAll()
	f.forgetRollups()

	// Four replicas fold at once: SKIP LOCKED gives each row to one.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var folded int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := f.ledger.FoldInsights(f.ctx)
			if err != nil {
				t.Errorf("FoldInsights: %v", err)
			}
			mu.Lock()
			folded += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	unique := f.count(`SELECT count(*) FROM llm_call_usage`) + f.count(`SELECT count(*) FROM tool_call_usage`)
	if folded != int64(unique) {
		t.Errorf("the replicas folded %d rows, want each of the %d once", folded, unique)
	}
	first := f.readAll()
	for _, table := range insightsTables {
		assertRollup(t, table.name+" (backfilled)", want[table.name], first[table.name])
	}

	if n := f.fold(); n != 0 {
		t.Errorf("a second backfill folded %d rows, want 0", n)
	}
	second := f.readAll()
	for _, table := range insightsTables {
		assertRollup(t, table.name+" (backfilled twice)", first[table.name], second[table.name])
	}
}

// TestInsightsPruneKeepsUnfoldedRows: retention deletes a ledger row only
// once the rollups count it, so a row from before them is never lost to the
// rollups by being pruned first.
func TestInsightsPruneKeepsUnfoldedRows(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	f.model(store.LLMCallUsageRecord{ID: "old", Attempt: 1, UserID: "alice", OrgID: "org-1", ChatID: "c", Provider: "p",
		Model: "m", AuthMode: "byok", Purpose: "chat", Outcome: store.LLMOutcomeSuccess, CreatedAt: insightsAt})
	f.tool(store.ToolCallUsageRecord{ID: "old", Attempt: 1, UserID: "alice", OrgID: "org-1", ChatID: "c",
		ToolName: "shell", ToolKind: store.ToolKindBuiltin, Location: "daemon", Outcome: store.ToolOutcomeSuccess, CreatedAt: insightsAt})
	f.forgetRollups()

	later := insightsAt.Add(llmusage.Retention + 24*time.Hour) // fixture time, not a clock
	if _, err := f.ledger.PruneLLMCallUsage(f.ctx, later); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ledger.PruneToolCallUsage(f.ctx, later); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM llm_call_usage`) + f.count(`SELECT count(*) FROM tool_call_usage`); n != 2 {
		t.Fatalf("pruning deleted unfolded rows: %d left, want 2", n)
	}
	f.fold()
	if _, err := f.ledger.PruneLLMCallUsage(f.ctx, later); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ledger.PruneToolCallUsage(f.ctx, later); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM llm_call_usage`) + f.count(`SELECT count(*) FROM tool_call_usage`); n != 0 {
		t.Fatalf("%d folded rows past retention survived the prune, want 0", n)
	}
	if n := f.count(`SELECT sum(calls) FROM insights_model_calls_daily`) + f.count(`SELECT sum(calls) FROM insights_tool_calls_daily`); n != 2 {
		t.Fatalf("the rollups hold %d calls after the prune, want 2", n)
	}
}

// TestProjectDeleteTakesTheInsightsMemberRows: a project's deletion removes
// the member rows that name it, keeps another project's, and a write that lands after the delete adds no member row for the
// project, whether it is a live write or the backfill of an older row.
func TestProjectDeleteTakesTheInsightsMemberRows(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t, "proj-del", "proj-keep")
	call := func(id, project string) store.LLMCallUsageRecord {
		return store.LLMCallUsageRecord{ID: id, Attempt: 1, UserID: storetest.SeedUserID, OrgID: "org-1", ChatID: "c-" + project,
			ProjectID: project, Provider: "p", Model: "m", AuthMode: "managed", Purpose: "chat", Funding: "org_wallet",
			Outcome: store.LLMOutcomeSuccess, CreatedAt: insightsAt}
	}
	f.model(call("a", "proj-del"))
	f.model(call("b", "proj-keep"))
	f.tool(store.ToolCallUsageRecord{ID: "t", Attempt: 1, UserID: storetest.SeedUserID, OrgID: "org-1", ChatID: "c-proj-del",
		ProjectID: "proj-del", ToolName: "shell", ToolKind: store.ToolKindBuiltin, Location: "daemon",
		Outcome: store.ToolOutcomeSuccess, CreatedAt: insightsAt})
	// An older call of the project that the rollups do not count yet.
	f.model(call("old", "proj-del"))
	f.exec(`UPDATE llm_call_usage SET insights_folded = false WHERE id = 'old'`)
	if n := f.count(`SELECT count(*) FROM insights_tool_calls_member_daily WHERE project_id = 'proj-del'`) +
		f.count(`SELECT count(*) FROM insights_model_calls_member_daily WHERE project_id = 'proj-del'`); n != 2 {
		t.Fatalf("member rows of the project before the delete: %d, want 2", n)
	}

	if err := store.DeleteUserProject(f.ctx, f.env.ORM, "proj-del", storetest.SeedUserID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for _, table := range insightsMemberTableNames {
		if n := f.count(`SELECT count(*) FROM ` + table + ` WHERE project_id = 'proj-del'`); n != 0 {
			t.Errorf("%s: %d rows name the deleted project, want 0", table, n)
		}
	}
	if n := f.count(`SELECT count(*) FROM insights_model_calls_member_daily WHERE project_id = 'proj-keep'`); n != 1 {
		t.Errorf("the other project's member rows: %d, want 1", n)
	}
	if n := f.count(`SELECT count(*) FROM insights_model_calls_daily WHERE project_id = 'proj-del'`); n != 0 {
		t.Errorf("the org's team rows still name the deleted project: %d, want 0", n)
	}

	f.model(call("late", "proj-del"))
	f.fold()
	if n := f.count(`SELECT count(*) FROM insights_model_calls_member_daily WHERE project_id = 'proj-del'`); n != 0 {
		t.Errorf("a write after the delete added %d member rows for the deleted project, want 0", n)
	}
}

var insightsMemberTableNames = []string{modelMember.name, toolMember.name}

// TestInsightsLatencyBucketMatchesSQL holds store.InsightsLatencyBucket, which
// a reader uses to turn a histogram into a percentile, to the SQL function
// that fills the histograms, across every bucket edge.
func TestInsightsLatencyBucketMatchesSQL(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	values := []int64{-5, 0}
	for k := 0; k <= 50; k++ {
		for _, d := range []int64{-1, 0, 1} {
			v := int64(1)<<(k/2) + d
			if k%2 == 1 {
				v = int64(float64(int64(1)<<(k/2))*1.41421356237) + d
			}
			values = append(values, v)
		}
	}
	values = append(values, 1<<62)
	rows, err := f.env.SQL.Query(`SELECT v, insights_latency_bucket(v) FROM unnest($1::bigint[]) AS v`, pgInt64Array(values))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var v int64
		var got int
		if err := rows.Scan(&v, &got); err != nil {
			t.Fatal(err)
		}
		seen++
		if want := store.InsightsLatencyBucket(v); got != want {
			t.Errorf("insights_latency_bucket(%d) = %d, store.InsightsLatencyBucket = %d", v, got, want)
		}
		lo, hi := store.InsightsLatencyBucketBounds(got)
		if v >= 1 && (float64(v) < lo || float64(v) >= hi) {
			t.Errorf("%d ms is in bucket %d, whose bounds are [%g, %g)", v, got, lo, hi)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(values) {
		t.Fatalf("read %d values, want %d", seen, len(values))
	}
}

func pgInt64Array(v []int64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.FormatInt(x, 10)
	}
	return "{" + strings.Join(s, ",") + "}"
}

// TestSweepTakesInsightsMemberRowsOfAGoneProject: an account purge deletes
// the account's projects, and with them its own member rows, but another
// member's rows of those projects, and the org's team rows, are left naming a
// project that is gone. The orphan sweep removes the member rows, leaving a
// live project's rows and rows with no project, and merges the team rows into
// the no-project bucket.
func TestSweepTakesInsightsMemberRowsOfAGoneProject(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t, "proj-live")
	for _, p := range []string{"proj-gone", "proj-live", ""} {
		f.exec(`INSERT INTO insights_model_calls_member_daily (user_id, day, project_id, provider, model, auth_mode, purpose, outcome,
			calls, latency_ms_hist, ttft_ms_hist)
			VALUES ('bob', $1, $2, 'p', 'm', 'byok', 'chat', 'success', 1, array_fill(0::bigint, ARRAY[48]), array_fill(0::bigint, ARRAY[48]))`,
			insightsAt, p)
		f.exec(`INSERT INTO insights_tool_calls_member_daily (user_id, day, project_id, tool_kind, location, outcome, calls, duration_ms_hist)
			VALUES ('bob', $1, $2, 'mcp', 'daemon', 'success', 1, array_fill(0::bigint, ARRAY[48]))`, insightsAt, p)
	}
	// The org's team rows of the gone project, one meeting a no-project row.
	for _, row := range []struct {
		project, model string
		calls          int
	}{{"proj-gone", "m", 2}, {"", "m", 3}, {"proj-gone", "m2", 4}} {
		f.exec(`INSERT INTO insights_model_calls_daily (org_id, day, project_id, provider, model, auth_mode, purpose, outcome,
			calls, latency_ms_hist, ttft_ms_hist)
			VALUES ('org-1', $1, $2, 'p', $3, 'byok', 'chat', 'success', $4, array_fill(1::bigint, ARRAY[48]), array_fill(0::bigint, ARRAY[48]))`,
			insightsAt, row.project, row.model, row.calls)
	}
	if _, err := store.SweepOrphanedProjectRows(f.ctx, f.env.ORM, 100); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	team := f.read(modelTeam)
	if len(team) != 2 || !strings.HasPrefix(team["org-1|2026-11-20|||||p|m|byok||f|chat|success|"], "5|") ||
		!strings.HasPrefix(team["org-1|2026-11-20|||||p|m2|byok||f|chat|success|"], "4|") {
		t.Errorf("team rows after the sweep: %v, want the gone project's merged into the no-project rows (5 and 4 calls)", team)
	}
	if got := team["org-1|2026-11-20|||||p|m|byok||f|chat|success|"]; !strings.HasSuffix(strings.Split(got, "|")[14], ",2") {
		t.Errorf("merged latency histogram %q, want each bucket summed (2)", got)
	}
	for _, table := range insightsMemberTableNames {
		var left []string
		rows, err := f.env.SQL.Query(`SELECT project_id FROM ` + table + ` ORDER BY project_id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			left = append(left, p)
		}
		rows.Close()
		if !slices.Equal(left, []string{"", "proj-live"}) {
			t.Errorf("%s after the sweep: %q, want the live project's row and the row with no project", table, left)
		}
	}
}

// teamTotalsIgnoringProject is table's counts summed over every key but the
// project: an org's totals, which a project's deletion must not change.
func (f *insightsFixture) teamTotalsIgnoringProject(table insightsTableShape) map[string]string {
	f.t.Helper()
	var keyCols, valCols []string
	for _, c := range table.key {
		switch c {
		case "project_id":
		case "day":
			keyCols = append(keyCols, "to_char(day, 'YYYY-MM-DD')")
		default:
			keyCols = append(keyCols, c)
		}
	}
	for _, c := range table.measures {
		if strings.HasSuffix(c, "_hist") {
			valCols = append(valCols, "array_to_string(insights_hist_sum("+c+"), ',')")
		} else {
			valCols = append(valCols, "sum("+c+")")
		}
	}
	rows, err := f.env.SQL.Query(`SELECT concat_ws('|', ` + strings.Join(keyCols, ", ") + `), concat_ws('|', ` +
		strings.Join(valCols, ", ") + `) FROM ` + table.name + ` GROUP BY 1`)
	if err != nil {
		f.t.Fatalf("totals of %s: %v", table.name, err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			f.t.Fatal(err)
		}
		got[k] = v
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return got
}

// TestProjectDeleteMergesTeamRowsIntoNoProject: deleting a project moves the
// org's team rows that name it into the no-project bucket of the same org,
// day and other keys, adding their counts to a row already there. The org's
// totals are the same before and after, another project's rows are
// untouched, and no row anywhere names the deleted project, including the
// row a later fold of one of its calls writes.
func TestProjectDeleteMergesTeamRowsIntoNoProject(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t, "proj-del", "proj-keep")
	r := rand.New(rand.NewPCG(11, 0xA4))
	models := randomModelCalls(r, "del", 120)
	tools := randomToolCalls(r, "del", 120)
	// Narrow the keys so the deleted project's rows meet rows already in the
	// no-project bucket, as well as making new ones.
	for i := range models {
		models[i].UserID, models[i].OrgID = storetest.SeedUserID, "org-1"
		models[i].WorkflowName, models[i].NodeID, models[i].Preset = "agent", "", ""
		models[i].ProjectID = []string{"proj-del", "proj-keep", ""}[i%3]
	}
	for i := range tools {
		tools[i].UserID, tools[i].OrgID = storetest.SeedUserID, "org-1"
		tools[i].WorkflowName, tools[i].NodeID, tools[i].Preset = "agent", "", ""
		tools[i].ProjectID = []string{"proj-del", "proj-keep", ""}[i%3]
	}
	for _, rec := range models {
		f.model(rec)
	}
	for _, rec := range tools {
		f.tool(rec)
	}
	// One of the project's calls the rollups do not count yet.
	late := models[0]
	late.ID, late.ProjectID = "late", "proj-del"
	f.model(late)
	f.exec(`UPDATE llm_call_usage SET insights_folded = false WHERE id = 'late'`)
	f.exec(`DELETE FROM insights_model_calls_daily`)
	f.exec(`DELETE FROM insights_model_calls_member_daily`)
	f.exec(`UPDATE llm_call_usage SET insights_folded = false`)
	f.fold()

	teams := []insightsTableShape{modelTeam, toolTeam}
	before := map[string]map[string]string{}
	keep := map[string]map[string]string{}
	merging := 0
	for _, table := range teams {
		before[table.name] = f.teamTotalsIgnoringProject(table)
		keep[table.name] = f.readProject(table, "proj-keep")
		merging += f.count(`SELECT count(*) FROM ` + table.name + ` d WHERE project_id = 'proj-del' AND EXISTS (
			SELECT 1 FROM ` + table.name + ` n WHERE n.project_id = '' AND n.org_id = d.org_id AND n.day = d.day)`)
	}
	if merging == 0 {
		t.Fatal("the fixture has no deleted-project row that meets a no-project row; it would not prove the merge")
	}

	if err := store.DeleteUserProject(f.ctx, f.env.ORM, "proj-del", storetest.SeedUserID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for _, table := range teams {
		assertRollup(t, table.name+" (org totals)", before[table.name], f.teamTotalsIgnoringProject(table))
		assertRollup(t, table.name+" (the other project)", keep[table.name], f.readProject(table, "proj-keep"))
	}
	f.exec(`UPDATE llm_call_usage SET insights_folded = false WHERE id = 'late'`)
	f.fold() // a fold after the delete, of a call that names the project
	for _, table := range insightsTables {
		if n := f.count(`SELECT count(*) FROM ` + table.name + ` WHERE project_id = 'proj-del'`); n != 0 {
			t.Errorf("%s: %d rows name the deleted project, want 0", table.name, n)
		}
	}
}

// readProject is the table's rows of one project.
func (f *insightsFixture) readProject(table insightsTableShape, projectID string) map[string]string {
	f.t.Helper()
	out := map[string]string{}
	for k, v := range f.read(table) {
		if strings.Split(k, "|")[2] == projectID {
			out[k] = v
		}
	}
	return out
}

// TestInsightsTeamCostCountsOnlyThePayerOrg: a team row counts a call's cost
// only when the org it belongs to is the org that paid. The work's org and the
// payer are separate: org-funded work in org A whose payer is unknown (the
// org-less managed key, or a row from before funding_org_id) or is org B is
// counted as calls in A without cost, and never lands in B's rows either;
// only a call A paid for is A's spend. The member row keeps the cost, which
// is the member's own view.
func TestInsightsTeamCostCountsOnlyThePayerOrg(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	call := func(id, payer string, cost int64) store.LLMCallUsageRecord {
		return store.LLMCallUsageRecord{ID: id, Attempt: 1, UserID: "alice", OrgID: "org-a", ChatID: "c", Provider: "reliant",
			Model: id, AuthMode: "managed", Purpose: "chat", Funding: "org_wallet", FundingOrgID: payer,
			Outcome: store.LLMOutcomeSuccess, CostUSDNanos: &cost, CostBasis: store.LLMCostBasisReported, CreatedAt: insightsAt}
	}
	f.model(call("unknown-payer", "", 100))
	f.model(call("paid-by-b", "org-b", 200))
	f.model(call("paid-by-a", "org-a", 400))
	// A row from before funding_org_id, folded by the backfill: unknown too.
	f.model(call("historical", "org-a", 800))
	f.exec(`UPDATE llm_call_usage SET funding_org_id = NULL, insights_folded = false WHERE id = 'historical'`)
	f.exec(`DELETE FROM insights_model_calls_daily WHERE model = 'historical'`)
	f.exec(`DELETE FROM insights_model_calls_member_daily WHERE model = 'historical'`)
	f.fold()

	for _, c := range []struct {
		model               string
		reportedCalls, cost int64
	}{{"unknown-payer", 0, 0}, {"paid-by-b", 0, 0}, {"paid-by-a", 1, 400}, {"historical", 0, 0}} {
		var calls, reported, cost int64
		if err := f.env.SQL.QueryRow(`SELECT calls, reported_calls, reported_cost_usd_nanos FROM insights_model_calls_daily
			WHERE org_id = 'org-a' AND model = $1`, c.model).Scan(&calls, &reported, &cost); err != nil {
			t.Fatalf("%s: team row: %v", c.model, err)
		}
		if calls != 1 || reported != c.reportedCalls || cost != c.cost {
			t.Errorf("%s: org-a's team row calls=%d reported_calls=%d cost=%d, want 1, %d, %d",
				c.model, calls, reported, cost, c.reportedCalls, c.cost)
		}
		var memberCost int64
		var paid bool
		if err := f.env.SQL.QueryRow(`SELECT reported_cost_usd_nanos, paid_by_org FROM insights_model_calls_member_daily
			WHERE user_id = 'alice' AND model = $1`, c.model).Scan(&memberCost, &paid); err != nil {
			t.Fatalf("%s: member row: %v", c.model, err)
		}
		if memberCost == 0 {
			t.Errorf("%s: the member's own row lost the call's cost", c.model)
		}
		// A reader of member rows (an admin's drill-down) finds the org's
		// spend by paid_by_org, never by the funding class alone.
		if want := c.reportedCalls == 1; paid != want {
			t.Errorf("%s: member row paid_by_org = %v, want %v", c.model, paid, want)
		}
	}
	if n := f.count(`SELECT count(*) FROM insights_model_calls_daily WHERE org_id = 'org-b'`); n != 0 {
		t.Errorf("org-b paid for work in org-a and got %d team rows; the work is org-a's", n)
	}
	var payer *string
	if err := f.env.SQL.QueryRow(`SELECT funding_org_id FROM llm_call_usage WHERE id = 'paid-by-b'`).Scan(&payer); err != nil {
		t.Fatal(err)
	}
	if payer == nil || *payer != "org-b" {
		t.Errorf("llm_call_usage.funding_org_id = %v, want the payer org-b kept in the ledger", payer)
	}
}
