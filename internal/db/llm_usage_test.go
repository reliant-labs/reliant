// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llmusage/llmusagetest"
	"github.com/reliant-labs/reliant/internal/store"
)

func usageRec(id string, at time.Time) LLMCallUsage {
	return LLMCallUsage{
		ID: id, UserID: "u1", ChatID: "chat-1", RunID: "run-1",
		Provider: "anthropic", Model: "claude-4.8-opus", AuthMode: store.LLMAuthModeBYOK, Purpose: store.LLMPurposeChat,
		InputTokens: 100, OutputTokens: 40, CacheReadTokens: 10, CacheWriteTokens: 5,
		Outcome: store.LLMOutcomeSuccess, LatencyMs: 250, Attempt: 1, CreatedAt: at,
	}
}

func rowCount(t *testing.T, raw *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(query, args...).Scan(&n))
	return n
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

// Temporal re-runs an activity under the same id after a failure. Every attempt
// is a real request to the provider, and a failed one can have used tokens (a
// stream reports usage and then breaks), so the ledger counts each attempt.
// Only a repeated write of the same attempt is ignored.
func TestLLMCallUsage_EveryActivityAttemptIsCountedAndRepeatsAreNot(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	at := day(2026, 10, 29)

	// Attempt 1 failed after the provider had billed 300 input and 80 output tokens.
	first := usageRec("wf/run/act-1", at)
	first.Outcome, first.ErrorClass, first.InputTokens, first.OutputTokens = store.LLMOutcomeError, "network", 300, 80
	first.CacheReadTokens, first.CacheWriteTokens, first.LatencyMs = 0, 0, 900
	written, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, first)
	require.NoError(t, err)
	assert.True(t, written)

	// The same attempt delivered again (a duplicate write): nothing changes.
	written, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, first)
	require.NoError(t, err)
	assert.False(t, written, "a repeat of the same attempt must not write")
	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))
	assert.Equal(t, 1, rowCount(t, raw, `SELECT calls FROM llm_call_usage_daily`))

	// Attempt 2 of the same activity succeeds: a second provider call, added to
	// the first, not a replacement of it.
	second := usageRec("wf/run/act-1", at)
	second.Attempt, second.InputTokens, second.OutputTokens = 2, 120, 60
	written, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, second)
	require.NoError(t, err)
	assert.True(t, written, "a later attempt is a call of its own")

	// A late delivery of attempt 1 after attempt 2 changes nothing.
	written, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, first)
	require.NoError(t, err)
	assert.False(t, written)

	assert.Equal(t, 2, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`), "two attempts, two provider calls")
	var attempts int
	require.NoError(t, raw.QueryRow(`SELECT count(DISTINCT attempt) FROM llm_call_usage WHERE id = $1`, "wf/run/act-1").Scan(&attempts))
	assert.Equal(t, 2, attempts)

	days, err := repo.ListLLMUsageDaily(ctx, "u1", at, at)
	require.NoError(t, err)
	require.Len(t, days, 1)
	d := days[0]
	assert.EqualValues(t, 2, d.Calls, "the failed attempt's request is still a request")
	assert.EqualValues(t, 1, d.ErrorCalls, "and its failure is still an error")
	assert.EqualValues(t, 300+120, d.InputTokens, "the tokens the failed attempt consumed are not lost")
	assert.EqualValues(t, 80+60, d.OutputTokens)
	assert.EqualValues(t, 900+250, d.LatencyMsTotal)
}

// Two workers delivering the same attempt at once (a duplicate delivery) must
// still leave exactly one call.
func TestLLMCallUsage_ConcurrentDuplicatesCountOnce(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	rec := usageRec("wf/run/act-race", day(2026, 10, 29))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, rec)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))
	assert.Equal(t, 1, rowCount(t, raw, `SELECT calls FROM llm_call_usage_daily`))
}

// The ledger records counts and metadata. This pins the exact column set of
// both tables, so a column that could hold a prompt, a reply, a tool input, a
// file name or an error's text cannot be added without changing this list and
// the privacy page (docs/help/privacy.mdx) with it.
func TestLLMUsageTablesHoldNoContent(t *testing.T) {
	t.Parallel()
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	want := map[string]map[string]string{
		"llm_call_usage": {
			"id": "text", "user_id": "text", "org_id": "text", "chat_id": "text", "run_id": "text",
			"provider": "text", "model": "text", "auth_mode": "text", "purpose": "text", "outcome": "text", "error_class": "text",
			"input_tokens": "bigint", "output_tokens": "bigint", "cache_read_tokens": "bigint",
			"cache_write_tokens": "bigint", "latency_ms": "bigint", "attempt": "integer",
			"created_at": "timestamp with time zone", "funding": "text", "account_id": "text",
			// What the call was part of (agent-analytics.md §3.2): opaque ids
			// and the workflow, node and preset names, the provider's request
			// id, time to first token, and cost on a closed-set basis (§4).
			"workflow_id": "text", "thread_id": "text", "workflow_name": "text", "node_id": "text", "preset": "text",
			"project_id": "text", "ai_run_id": "text", "ai_root_run_id": "text", "provider_request_id": "text",
			"ttft_ms": "bigint", "cost_usd_nanos": "bigint", "cost_basis": "text",
		},
		"llm_call_usage_daily": {
			"user_id": "text", "org_id": "text", "provider": "text", "model": "text", "auth_mode": "text", "purpose": "text",
			"day": "date", "calls": "bigint", "error_calls": "bigint", "input_tokens": "bigint",
			"output_tokens": "bigint", "cache_read_tokens": "bigint", "cache_write_tokens": "bigint",
			"latency_ms_total": "bigint", "updated_at": "timestamp with time zone",
		},
		// The org-funded rollup an org admin reads (org-provider-accounts.md §3.7): ids,
		// closed-set labels and counts.
		"llm_org_usage_daily": {
			"day": "date", "org_id": "text", "account_id": "text", "user_id": "text", "provider": "text",
			"model": "text", "funding": "text", "purpose": "text", "calls": "bigint", "error_calls": "bigint",
			"input_tokens": "bigint", "output_tokens": "bigint", "cache_read_tokens": "bigint",
			"cache_write_tokens": "bigint", "latency_ms_total": "bigint", "updated_at": "timestamp with time zone",
		},
		"llm_usage_purged_users": {"user_id": "text", "purged_at": "timestamp with time zone"},
	}
	for table, cols := range want {
		rows, err := raw.Query(`SELECT column_name, data_type FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1`, table)
		require.NoError(t, err)
		got := map[string]string{}
		for rows.Next() {
			var name, typ string
			require.NoError(t, rows.Scan(&name, &typ))
			got[name] = typ
		}
		require.NoError(t, rows.Err())
		rows.Close()
		assert.Equal(t, cols, got, "%s: the column set is the privacy page's list of what is recorded", table)
	}

	// error_class is a closed set, so a provider's error message cannot be
	// stored in it.
	_, err := raw.Exec(`INSERT INTO llm_call_usage (id, user_id, chat_id, provider, model, auth_mode, purpose, outcome, error_class, attempt)
		VALUES ('x', 'u', 'c', 'p', 'm', 'byok', 'chat', 'error', 'invalid api key sk-ant-secret', 1)`)
	assert.Error(t, err, "free text in error_class must be refused by the schema")

	// funding is a closed set too, on the ledger and the org rollup; the rollup holds
	// org-funded calls only, never a personal one.
	_, err = raw.Exec(`INSERT INTO llm_call_usage (id, user_id, chat_id, provider, model, auth_mode, purpose, outcome, attempt, funding)
		VALUES ('y', 'u', 'c', 'p', 'm', 'byok', 'chat', 'success', 1, 'acme prod key')`)
	assert.Error(t, err, "free text in funding must be refused by the schema")

	// cost_basis is a closed set, and a cost is present exactly when the
	// basis prices the call: no cost without a basis, none on an unpriced
	// call, and no priced basis without its cost.
	costRow := func(id, basis string, nanos any) error {
		var b any
		if basis != "" {
			b = basis
		}
		_, err := raw.Exec(`INSERT INTO llm_call_usage (id, user_id, chat_id, provider, model, auth_mode, purpose, outcome, attempt, cost_basis, cost_usd_nanos)
			VALUES ($1, 'u', 'c', 'p', 'm', 'byok', 'chat', 'success', 1, $2, $3)`, id, b, nanos)
		return err
	}
	assert.Error(t, costRow("cb-free", "list price from a blog", int64(5)), "free text in cost_basis must be refused by the schema")
	assert.Error(t, costRow("cb-nobasis", "", int64(0)), "a cost with no basis must be refused: it would read as a price")
	assert.Error(t, costRow("cb-unpriced", "unpriced", int64(0)), "an unpriced call must not carry a cost")
	assert.Error(t, costRow("cb-reported", "reported", nil), "a reported basis must carry its cost")
	assert.Error(t, costRow("cb-negative", "reported", int64(-1)), "a cost is never negative")
	assert.NoError(t, costRow("cb-ok-reported", "reported", int64(12_300_000)))
	assert.NoError(t, costRow("cb-ok-unpriced", "unpriced", nil))
	assert.NoError(t, costRow("cb-ok-unknown", "", nil))
	_, err = raw.Exec(`INSERT INTO llm_org_usage_daily (day, org_id, account_id, user_id, provider, model, funding, purpose)
		VALUES (current_date, 'o', 'a', 'u', 'p', 'm', 'personal', 'chat')`)
	assert.Error(t, err, "the org rollup must refuse a personal-account row")
	_, err = raw.Exec(`INSERT INTO llm_org_usage_daily (day, org_id, account_id, user_id, provider, model, funding, purpose)
		VALUES (current_date, 'o', 'a', 'u', 'p', 'm', 'org_key', 'chat')`)
	assert.NoError(t, err)
}

func TestLLMUsageDaily_RollupSumsPerUserModelProviderAuthModeAndDay(t *testing.T) {
	t.Parallel()
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	d1, d2 := day(2026, 10, 28), day(2026, 10, 29)
	mk := func(id string, at time.Time, mut func(*LLMCallUsage)) {
		r := usageRec(id, at)
		mut(&r)
		_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, r)
		require.NoError(t, err)
	}
	mk("a", d1, func(r *LLMCallUsage) {})
	mk("b", d1, func(r *LLMCallUsage) { r.InputTokens, r.OutputTokens, r.LatencyMs = 300, 70, 750 })
	mk("c", d1, func(r *LLMCallUsage) {
		r.Outcome, r.ErrorClass, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens = store.LLMOutcomeError, "rate_limit", 0, 0, 0, 0
	})
	mk("d", d1, func(r *LLMCallUsage) { r.Model = "claude-4.8-sonnet" })
	mk("e", d1, func(r *LLMCallUsage) { r.Provider, r.AuthMode = "claude", store.LLMAuthModeSubscription })
	mk("f", d2, func(r *LLMCallUsage) {})
	mk("g", d1, func(r *LLMCallUsage) { r.UserID = "someone-else" })
	// 23:59 UTC and 00:01 UTC fall on different days whatever the server's zone.
	mk("h", time.Date(2026, 10, 28, 23, 59, 0, 0, time.UTC), func(r *LLMCallUsage) { r.Model = "edge" })
	mk("i", time.Date(2026, 10, 29, 0, 1, 0, 0, time.UTC), func(r *LLMCallUsage) { r.Model = "edge" })

	rows, err := repo.ListLLMUsageDaily(ctx, "u1", d1, d2)
	require.NoError(t, err)
	type key struct{ day, provider, model, mode string }
	got := map[key]LLMUsageDay{}
	for _, r := range rows {
		assert.Equal(t, "u1", r.UserID, "the rollup read is the caller's own rows only")
		got[key{r.Day.Format("2006-01-02"), r.Provider, r.Model, r.AuthMode}] = *r
	}

	opus := got[key{"2026-10-28", "anthropic", "claude-4.8-opus", "byok"}]
	assert.EqualValues(t, 3, opus.Calls)
	assert.EqualValues(t, 1, opus.ErrorCalls)
	assert.EqualValues(t, 400, opus.InputTokens)
	assert.EqualValues(t, 110, opus.OutputTokens)
	assert.EqualValues(t, 20, opus.CacheReadTokens)
	assert.EqualValues(t, 10, opus.CacheWriteTokens)
	assert.EqualValues(t, 250+750+250, opus.LatencyMsTotal)

	assert.EqualValues(t, 1, got[key{"2026-10-28", "anthropic", "claude-4.8-sonnet", "byok"}].Calls, "a model has its own row")
	assert.EqualValues(t, 1, got[key{"2026-10-28", "claude", "claude-4.8-opus", "subscription"}].Calls, "provider and auth mode split the rollup")
	assert.EqualValues(t, 1, got[key{"2026-10-29", "anthropic", "claude-4.8-opus", "byok"}].Calls, "a day has its own row")
	assert.EqualValues(t, 1, got[key{"2026-10-28", "anthropic", "edge", "byok"}].Calls, "23:59 UTC is the 28th")
	assert.EqualValues(t, 1, got[key{"2026-10-29", "anthropic", "edge", "byok"}].Calls, "00:01 UTC is the 29th")

	var keys []string
	for k := range got {
		keys = append(keys, fmt.Sprint(k))
	}
	sort.Strings(keys)
	assert.Len(t, got, 6, "rows: %v", keys)

	// Range bounds are inclusive UTC dates.
	only, err := repo.ListLLMUsageDaily(ctx, "u1", d2, d2)
	require.NoError(t, err)
	for _, r := range only {
		assert.Equal(t, "2026-10-29", r.Day.Format("2006-01-02"))
	}
}

// Retention deletes the per-call rows past 90 days and nothing else.
func TestLLMCallUsage_RetentionDeletesCallRowsAndKeepsTheRollup(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	now := day(2026, 10, 29)

	old := now.Add(-LLMCallUsageRetention - time.Hour)
	edge := now.Add(-LLMCallUsageRetention + time.Hour)
	for i, at := range []time.Time{old, old.Add(-48 * time.Hour), edge, now} {
		_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, usageRec(fmt.Sprintf("r%d", i), at))
		require.NoError(t, err)
	}
	require.Equal(t, 4, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))
	totalCalls := func() int {
		return rowCount(t, raw, `SELECT COALESCE(sum(calls), 0) FROM llm_call_usage_daily`)
	}
	require.Equal(t, 4, totalCalls())

	n, err := llmusagetest.New(t, repo).PruneLLMCallUsage(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	assert.Equal(t, 2, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`), "rows inside the period stay")
	assert.Equal(t, 4, totalCalls(), "the rollup keeps the deleted rows' totals")

	n, err = llmusagetest.New(t, repo).PruneLLMCallUsage(ctx, now)
	require.NoError(t, err)
	assert.Zero(t, n, "a second pass has nothing to delete")

}

func TestLLMCallUsage_PruneWorksInBatches(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	now := day(2026, 10, 29)
	for i := 0; i < 5; i++ {
		_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, usageRec(fmt.Sprintf("b%d", i), now.Add(-200*24*time.Hour)))
		require.NoError(t, err)
	}
	cutoff := now.Add(-LLMCallUsageRetention)
	for _, want := range []int64{2, 2, 1, 0} {
		n, err := store.PruneLLMCallUsage(ctx, repo.ORM(), cutoff, 2)
		require.NoError(t, err)
		assert.Equal(t, want, n)
	}
	assert.Zero(t, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))

	_, err := store.PruneLLMCallUsage(ctx, repo.ORM(), cutoff, 0)
	assert.Error(t, err, "a non-positive batch is a caller bug")
}

// Purpose splits the rollup, so a dashboard can tell a chat turn from a
// compaction or a title for as long as the rollup lives.
func TestLLMUsageDaily_PurposeSplitsTheRollup(t *testing.T) {
	t.Parallel()
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	at := day(2026, 10, 29)
	for i, purpose := range []string{store.LLMPurposeChat, store.LLMPurposeChat, store.LLMPurposeCompaction, store.LLMPurposeTitle, store.LLMPurposeDraftAutomation} {
		r := usageRec(fmt.Sprintf("p%d", i), at)
		r.Purpose = purpose
		_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, r)
		require.NoError(t, err)
	}
	rows, err := repo.ListLLMUsageDaily(ctx, "u1", at, at)
	require.NoError(t, err)
	calls := map[string]int64{}
	for _, r := range rows {
		calls[r.Purpose] += r.Calls
	}
	assert.Equal(t, map[string]int64{"chat": 2, "compaction": 1, "title": 1, "draft_automation": 1}, calls)
	assert.Len(t, rows, 4, "one rollup row per purpose")

	bad := usageRec("bad", at)
	bad.Purpose = "chatting"
	_, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, bad)
	assert.Error(t, err, "purpose is a closed set")
}

// A model call already in flight when its account is purged must not write
// usage back afterwards, in either table.
func TestLLMCallUsage_RefusedForAPurgedUser(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	at := day(2026, 10, 29)

	_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, usageRec("before", at))
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO llm_usage_purged_users (user_id) VALUES ('u1')`)
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM llm_call_usage WHERE user_id = 'u1'; DELETE FROM llm_call_usage_daily WHERE user_id = 'u1'`)
	require.NoError(t, err)

	written, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, usageRec("after", at))
	require.NoError(t, err)
	assert.False(t, written, "a call that finishes after deletion is not recorded")
	assert.Zero(t, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))
	assert.Zero(t, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage_daily`))

	later := usageRec("started-after-the-purge", at.Add(2*time.Hour))
	written, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, later)
	require.NoError(t, err)
	assert.False(t, written, "the fence refuses every write, whatever time the call started")

	other := usageRec("other-user", at)
	other.UserID = "u2"
	written, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, other)
	require.NoError(t, err)
	assert.True(t, written, "the fence is per user")
}

func TestLLMUsagePurgeFences_AgeOut(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	now := day(2026, 10, 29)
	_, err := raw.Exec(`INSERT INTO llm_usage_purged_users (user_id, purged_at) VALUES ('old', $1), ('recent', $2)`,
		now.Add(-store.LLMUsagePurgeFenceRetention-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	n, err := llmusagetest.New(t, repo).PruneLLMUsagePurgeFences(context.Background(), now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM llm_usage_purged_users WHERE user_id = 'recent'`))
}

// The run-scope columns (agent-analytics.md §3.2) round-trip, and a field the
// recorder left empty is NULL, not "": "not recorded" never reads as a value.
func TestLLMCallUsage_RunScopeColumnsRoundTripAndEmptyIsNull(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	ttft, nanos := int64(420), int64(12_300_000)
	full := usageRec("wf/run/act-scoped", day(2026, 11, 24))
	full.WorkflowID, full.ThreadID, full.WorkflowName, full.NodeID = "wf-1", "thread-1", "agent", "call_llm"
	full.Preset, full.ProjectID, full.AIRunID, full.AIRootRunID = "researcher", "proj-1", "run-child", "run-root"
	full.ProviderRequestID, full.TTFTMs = "req_abc", &ttft
	full.CostUSDNanos, full.CostBasis = &nanos, store.LLMCostBasisReported
	_, err := llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, full)
	require.NoError(t, err)
	_, err = llmusagetest.New(t, repo).RecordLLMCallUsage(ctx, usageRec("wf/run/act-bare", day(2026, 11, 24)))
	require.NoError(t, err)

	rows, err := store.ListLlmCallUsage(ctx, repo.ORM())
	require.NoError(t, err)
	byID := map[string]*store.LlmCallUsage{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	got := byID["wf/run/act-scoped"]
	require.NotNil(t, got)
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	assert.Equal(t, "wf-1", str(got.WorkflowID))
	assert.Equal(t, "thread-1", str(got.ThreadID))
	assert.Equal(t, "agent", str(got.WorkflowName))
	assert.Equal(t, "call_llm", str(got.NodeID))
	assert.Equal(t, "researcher", str(got.Preset))
	assert.Equal(t, "proj-1", str(got.ProjectID))
	assert.Equal(t, "run-child", str(got.AiRunID))
	assert.Equal(t, "run-root", str(got.AiRootRunID))
	assert.Equal(t, "req_abc", str(got.ProviderRequestID))
	require.NotNil(t, got.TtftMs)
	assert.EqualValues(t, 420, *got.TtftMs)
	require.NotNil(t, got.CostUsdNanos)
	assert.EqualValues(t, 12_300_000, *got.CostUsdNanos)
	assert.Equal(t, "reported", str(got.CostBasis))

	bare := byID["wf/run/act-bare"]
	require.NotNil(t, bare)
	for name, p := range map[string]*string{
		"workflow_id": bare.WorkflowID, "thread_id": bare.ThreadID, "workflow_name": bare.WorkflowName,
		"node_id": bare.NodeID, "preset": bare.Preset, "project_id": bare.ProjectID, "ai_run_id": bare.AiRunID,
		"ai_root_run_id": bare.AiRootRunID, "provider_request_id": bare.ProviderRequestID, "cost_basis": bare.CostBasis,
	} {
		assert.Nil(t, p, "%s: not recorded is NULL, not an empty value", name)
	}
	assert.Nil(t, bare.TtftMs, "no first token is NULL, not 0 ms")
	assert.Nil(t, bare.CostUsdNanos, "an unpriced call reads as no cost, not as $0")

	// Summed, an unpriced call adds nothing and is not counted as priced: the
	// total over unpriced calls alone is NULL, never 0.
	var total sql.NullInt64
	require.NoError(t, raw.QueryRow(`SELECT sum(cost_usd_nanos) FROM llm_call_usage WHERE id = $1`, "wf/run/act-bare").Scan(&total))
	assert.False(t, total.Valid, "the cost of unpriced calls is unknown, not zero")
	var priced, all int
	require.NoError(t, raw.QueryRow(`SELECT count(cost_usd_nanos), count(*) FROM llm_call_usage`).Scan(&priced, &all))
	assert.Equal(t, 1, priced, "only the priced call is counted as priced")
	assert.Equal(t, 2, all)
}
