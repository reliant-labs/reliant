// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for LlmCallUsage. The generated llm_call_usage_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: llmCallUsageRepo (a *crud.Repo[LlmCallUsage]) and the
//     package-level GetLlmCallUsageByID / ListLlmCallUsage / CreateLlmCallUsage / UpdateLlmCallUsage / DeleteLlmCallUsage
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     LlmCallUsage struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindLlmCallUsageBy(ctx context.Context, db orm.Context, /* args */) ([]*LlmCallUsage, error) {
//		var rows []*LlmCallUsage
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveLlmCallUsages(ctx context.Context, db orm.Context) ([]*LlmCallUsage, error) {
//		return ListLlmCallUsage(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// llm_call_usage (count-only per-call ledger) and llm_call_usage_daily (its rollup).
// Neither table has a content column; the schema test pins that.

// Auth modes: how the credential that made the call was obtained.
const (
	LLMAuthModeManaged      = "managed"
	LLMAuthModeBYOK         = "byok"
	LLMAuthModeSubscription = "subscription"
	LLMAuthModeLocal        = "local"
)

// What a model request was for.
const (
	LLMPurposeChat            = "chat"
	LLMPurposeCompaction      = "compaction"
	LLMPurposeTitle           = "title"
	LLMPurposeDraftAutomation = "draft_automation"
)

// Call outcomes.
const (
	LLMOutcomeSuccess   = "success"
	LLMOutcomeError     = "error"
	LLMOutcomeCancelled = "cancelled"
)

// LLMCallUsageRecord is one call's counts. ID is the activity's idempotency key.
type LLMCallUsageRecord struct {
	ID               string
	UserID           string
	OrgID            string
	ChatID           string
	RunID            string
	Provider         string
	Model            string
	AuthMode         string
	Purpose          string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Outcome          string
	ErrorClass       string
	LatencyMs        int64
	Attempt          int32
	CreatedAt        time.Time
}

// RecordLLMCallUsage writes one provider request and folds it into its day's
// rollup, in one transaction. A request is (ID, Attempt): Temporal re-runs an
// activity after a failure, and every attempt is a real call to the provider
// that may have used tokens, so each attempt is counted. Only a repeated write
// of the same attempt (a duplicate delivery) is ignored. Reports whether
// anything was written.
//
// A write for a call that began before its user's account was purged is refused
// (false, nil): a model call already in flight when the account was deleted must
// not recreate usage that the deletion promised to remove. Its start is its
// completion time minus its latency. The write holds a shared advisory lock
// on the user, so a purge (which takes it exclusively, see accountpurge) either
// waits for this write and then deletes its rows, or commits first and is seen
// here.
//
// The caller's ctx must be a transaction (db.Repo.RunTx) so the lock holds
// until the rollup is written.
func RecordLLMCallUsage(ctx context.Context, db orm.Context, rec LLMCallUsageRecord) (bool, error) {
	if rec.ID == "" || rec.UserID == "" {
		return false, errors.New("llm call usage: id and user id are required")
	}
	if rec.Attempt < 1 {
		rec.Attempt = 1
	}
	rec.CreatedAt = rec.CreatedAt.UTC()

	if _, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock_shared(hashtextextended(?, 0))`, LLMUsageUserLockKey(rec.UserID)).Exec(ctx); err != nil {
		return false, fmt.Errorf("lock llm call usage for user: %w", err)
	}
	// Only a call that began before the purge is refused: a user who signs up
	// again under the same id starts a new history, and their calls begin after.
	startedAt := rec.CreatedAt.Add(-time.Duration(rec.LatencyMs) * time.Millisecond)
	var purged bool
	if err := db.Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM llm_usage_purged_users WHERE user_id = ? AND purged_at > ?)`, rec.UserID, startedAt).Scan(ctx, &purged); err != nil {
		return false, fmt.Errorf("check purged user: %w", err)
	}
	if purged {
		return false, nil
	}

	res, err := db.Bun().NewRaw(`INSERT INTO llm_call_usage (id, user_id, org_id, chat_id, run_id, provider, model, auth_mode, purpose,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, outcome, error_class, latency_ms, attempt, created_at)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17)
		ON CONFLICT (id, attempt) DO NOTHING`,
		rec.ID, rec.UserID, rec.OrgID, rec.ChatID, rec.RunID, rec.Provider, rec.Model, rec.AuthMode, rec.Purpose,
		rec.InputTokens, rec.OutputTokens, rec.CacheReadTokens, rec.CacheWriteTokens, rec.Outcome, rec.ErrorClass,
		rec.LatencyMs, rec.Attempt, rec.CreatedAt).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("insert llm call usage %s: %w", rec.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, fmt.Errorf("insert llm call usage %s: %w", rec.ID, err)
	} else if n == 0 {
		return false, nil
	}
	if err := addLLMUsageDaily(ctx, db, rec); err != nil {
		return false, err
	}
	return true, nil
}

// LLMUsageUserLockKey is the advisory-lock key that serializes usage writes
// against an account purge for one user.
func LLMUsageUserLockKey(userID string) string { return "llm-usage-user:" + userID }

// addLLMUsageDaily adds one call to its UTC day's rollup row.
func addLLMUsageDaily(ctx context.Context, db orm.Context, rec LLMCallUsageRecord) error {
	_, err := db.Bun().NewRaw(`INSERT INTO llm_call_usage_daily AS d (user_id, day, org_id, provider, model, auth_mode, purpose,
		calls, error_calls, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, latency_ms_total, updated_at)
		VALUES (?0, (?1::timestamptz AT TIME ZONE 'UTC')::date, ?2, ?3, ?4, ?5, ?6, 1, ?7, ?8, ?9, ?10, ?11, ?12, now())
		ON CONFLICT (user_id, day, org_id, provider, model, auth_mode, purpose) DO UPDATE SET
		    calls = d.calls + EXCLUDED.calls,
		    error_calls = d.error_calls + EXCLUDED.error_calls,
		    input_tokens = d.input_tokens + EXCLUDED.input_tokens,
		    output_tokens = d.output_tokens + EXCLUDED.output_tokens,
		    cache_read_tokens = d.cache_read_tokens + EXCLUDED.cache_read_tokens,
		    cache_write_tokens = d.cache_write_tokens + EXCLUDED.cache_write_tokens,
		    latency_ms_total = d.latency_ms_total + EXCLUDED.latency_ms_total,
		    updated_at = now()`,
		rec.UserID, rec.CreatedAt.UTC(), rec.OrgID, rec.Provider, rec.Model, rec.AuthMode, rec.Purpose,
		llmErrorCalls(rec), rec.InputTokens, rec.OutputTokens, rec.CacheReadTokens,
		rec.CacheWriteTokens, rec.LatencyMs).Exec(ctx)
	if err != nil {
		return fmt.Errorf("update llm call usage rollup: %w", err)
	}
	return nil
}

func llmErrorCalls(rec LLMCallUsageRecord) int64 {
	if rec.Outcome == LLMOutcomeError {
		return 1
	}
	return 0
}

// PruneLLMCallUsage deletes per-call rows created before cutoff, at most batch
// of them, and returns how many. The daily rollup is untouched: it already holds
// their totals. Call it until it returns less than batch.
func PruneLLMCallUsage(ctx context.Context, db orm.Context, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		return 0, errors.New("prune llm call usage: batch must be positive")
	}
	res, err := db.Bun().NewRaw(`DELETE FROM llm_call_usage WHERE (id, attempt) IN (
		SELECT id, attempt FROM llm_call_usage WHERE created_at < ? ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED)`,
		cutoff.UTC(), batch).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune llm call usage: %w", err)
	}
	return res.RowsAffected()
}

// ListLLMUsageDaily returns the user's rollup rows for days in [from, to] (UTC
// dates), oldest first. It is what the usage API reads: the per-call rows are
// deleted after the retention period, the rollup is not.
func ListLLMUsageDaily(ctx context.Context, db orm.Context, userID string, from, to time.Time) ([]*LlmCallUsageDaily, error) {
	var rows []*LlmCallUsageDaily
	err := db.Bun().NewSelect().Model(&rows).
		Where("user_id = ?", userID).
		Where("day >= ?::date", from.UTC().Format("2006-01-02")).
		Where("day <= ?::date", to.UTC().Format("2006-01-02")).
		OrderExpr("day, provider, model, auth_mode, purpose, org_id").
		Scan(ctx)
	return rows, err
}

// DeleteLLMCallUsageForUser removes everything the ledger holds for a user, the
// rollup included. Account deletion calls it.
func DeleteLLMCallUsageForUser(ctx context.Context, db orm.Context, userID string) (int64, error) {
	var total int64
	for _, table := range []string{"llm_call_usage", "llm_call_usage_daily"} {
		res, err := db.Bun().NewRaw(`DELETE FROM `+table+` WHERE user_id = ?`, userID).Exec(ctx)
		if err != nil {
			return 0, fmt.Errorf("delete %s for user: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// LlmCallUsageExtra carries fields on LlmCallUsage that are NOT columns — computed
// values, transient state, anything derived rather than stored. LlmCallUsage is
// embedded on LlmCallUsageExtra (see llm_call_usage_orm.go), so a field added here is a field on
// every LlmCallUsage value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type LlmCallUsageExtra struct {
}

// LLMUsagePurgeFenceRetention is how long a purged user's fence row is kept:
// longer than an activity and all of its retries can run, so a call in flight
// at deletion has long finished (or been abandoned) by the time it goes.
const LLMUsagePurgeFenceRetention = 7 * 24 * time.Hour

// PruneLLMUsagePurgeFences deletes purge fences older than
// LLMUsagePurgeFenceRetention at now.
func PruneLLMUsagePurgeFences(ctx context.Context, db orm.Context, now time.Time) (int64, error) {
	res, err := db.Bun().NewRaw(`DELETE FROM llm_usage_purged_users WHERE purged_at < ?`,
		now.Add(-LLMUsagePurgeFenceRetention).UTC()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune llm usage purge fences: %w", err)
	}
	return res.RowsAffected()
}
