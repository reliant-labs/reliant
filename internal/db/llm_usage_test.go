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

// Temporal re-runs an activity under the same id after a failure or a worker
// loss. The ledger must count that as one call, and the later attempt's counts
// must be the ones that stand.
func TestLLMCallUsage_IdempotentUnderActivityRetry(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	at := day(2026, 10, 29)

	// Attempt 1 failed.
	first := usageRec("wf/run/act-1", at)
	first.Outcome, first.ErrorClass, first.InputTokens, first.OutputTokens = store.LLMOutcomeError, "network", 0, 0
	written, err := repo.RecordLLMCallUsage(ctx, first)
	require.NoError(t, err)
	assert.True(t, written)

	// The same attempt delivered again (a duplicate write): nothing changes.
	written, err = repo.RecordLLMCallUsage(ctx, first)
	require.NoError(t, err)
	assert.False(t, written, "a repeat of the same attempt must not write")
	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))
	assert.Equal(t, 1, rowCount(t, raw, `SELECT error_calls FROM llm_call_usage_daily`))

	// Attempt 2 of the same activity succeeds: it replaces attempt 1's row.
	rec := usageRec("wf/run/act-1", at)
	rec.Attempt, rec.InputTokens, rec.OutputTokens = 2, 120, 60
	written, err = repo.RecordLLMCallUsage(ctx, rec)
	require.NoError(t, err)
	assert.True(t, written, "a later attempt replaces the earlier one")

	// A late delivery of attempt 1 after attempt 2 is ignored.
	written, err = repo.RecordLLMCallUsage(ctx, first)
	require.NoError(t, err)
	assert.False(t, written, "an earlier attempt must not overwrite a later one")

	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`), "one activity is one call")
	var in, out int64
	var attempt int
	require.NoError(t, raw.QueryRow(`SELECT input_tokens, output_tokens, attempt FROM llm_call_usage WHERE id = $1`, rec.ID).Scan(&in, &out, &attempt))
	assert.Equal(t, [3]int64{120, 60, 2}, [3]int64{in, out, int64(attempt)})

	days, err := repo.ListLLMUsageDaily(ctx, "u1", at, at)
	require.NoError(t, err)
	require.Len(t, days, 1)
	assert.EqualValues(t, 1, days[0].Calls, "the rollup counts the call once, whatever the attempts")
	assert.EqualValues(t, 0, days[0].ErrorCalls, "the replaced failed attempt is out of the rollup")
	assert.EqualValues(t, 120, days[0].InputTokens)
	assert.EqualValues(t, 60, days[0].OutputTokens)
}

// Two workers delivering the same activity at once (a retry racing a slow
// first attempt) must still leave exactly one call.
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
			_, err := repo.RecordLLMCallUsage(ctx, rec)
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
			"created_at": "timestamp with time zone",
		},
		"llm_call_usage_daily": {
			"user_id": "text", "org_id": "text", "provider": "text", "model": "text", "auth_mode": "text",
			"day": "date", "calls": "bigint", "error_calls": "bigint", "input_tokens": "bigint",
			"output_tokens": "bigint", "cache_read_tokens": "bigint", "cache_write_tokens": "bigint",
			"latency_ms_total": "bigint", "updated_at": "timestamp with time zone",
		},
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
		_, err := repo.RecordLLMCallUsage(ctx, r)
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
		_, err := repo.RecordLLMCallUsage(ctx, usageRec(fmt.Sprintf("r%d", i), at))
		require.NoError(t, err)
	}
	require.Equal(t, 4, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`))
	totalCalls := func() int {
		return rowCount(t, raw, `SELECT COALESCE(sum(calls), 0) FROM llm_call_usage_daily`)
	}
	require.Equal(t, 4, totalCalls())

	n, err := repo.PruneLLMCallUsage(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	assert.Equal(t, 2, rowCount(t, raw, `SELECT count(*) FROM llm_call_usage`), "rows inside the period stay")
	assert.Equal(t, 4, totalCalls(), "the rollup keeps the deleted rows' totals")

	n, err = repo.PruneLLMCallUsage(ctx, now)
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
		_, err := repo.RecordLLMCallUsage(ctx, usageRec(fmt.Sprintf("b%d", i), now.Add(-200*24*time.Hour)))
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
