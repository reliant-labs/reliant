// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProviderBackoff. The generated provider_backoff_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: providerBackoffRepo (a *crud.Repo[ProviderBackoff]) and the
//     package-level GetProviderBackoffByID / ListProviderBackoff / CreateProviderBackoff / UpdateProviderBackoff / DeleteProviderBackoff
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProviderBackoff struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProviderBackoffBy(ctx context.Context, db orm.Context, /* args */) ([]*ProviderBackoff, error) {
//		var rows []*ProviderBackoff
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProviderBackoffs(ctx context.Context, db orm.Context) ([]*ProviderBackoff, error) {
//		return ListProviderBackoff(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// provider_backoff (P5b lane L6): one thread's provider backoff marker and its cumulative
// totals (see core.ProviderBackoff). A new wait charges the slept part of the previous one,
// LEAST(new start, previous resume_at) - previous start, to waited_ms; a clear charges the slept
// part of the open one. The row is kept after a clear: the totals outlive the run.

// RecordProviderBackoff marks the thread as parked in a driver's retry ladder from now until
// now+delay.
func RecordProviderBackoff(ctx context.Context, db orm.Context, chatID, threadID string, attempt, maxAttempts, statusCode int, reason string, delay time.Duration, now time.Time) error {
	now = now.UTC()
	_, err := db.Bun().NewRaw(`
INSERT INTO provider_backoff (
    thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
    status_code, reason, retries, waited_ms, updated_at
) VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, 1, 0, ?2)
ON CONFLICT (thread_id) DO UPDATE SET
    chat_id = EXCLUDED.chat_id,
    waiting_since = EXCLUDED.waiting_since,
    resume_at = EXCLUDED.resume_at,
    attempt = EXCLUDED.attempt,
    max_attempts = EXCLUDED.max_attempts,
    status_code = EXCLUDED.status_code,
    reason = EXCLUDED.reason,
    provider = '',
    retries = provider_backoff.retries + 1,
    waited_ms = provider_backoff.waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(provider_backoff.resume_at, EXCLUDED.waiting_since), EXCLUDED.waiting_since)
        - provider_backoff.waiting_since)) * 1000)::BIGINT),
    updated_at = EXCLUDED.updated_at`,
		threadID, chatID, now, now.Add(delay), orm.Arg(attempt), orm.Arg(maxAttempts), orm.Arg(statusCode), reason).Exec(ctx)
	return err
}

// ClearProviderBackoff releases the thread from a wait at now. A thread with no row, or not
// waiting, is a no-op.
func ClearProviderBackoff(ctx context.Context, db orm.Context, threadID string, now time.Time) error {
	_, err := clearProviderBackoff(db, now).Where("thread_id = ?", threadID).Exec(ctx)
	return err
}

// ClearUsageLimitWait releases the chat's threads from a usage-limit wait at now — one thread,
// or every thread of the chat when threadID is "" — and reports how many it released.
func ClearUsageLimitWait(ctx context.Context, db orm.Context, chatID, threadID string, now time.Time) (int64, error) {
	q := clearProviderBackoff(db, now).Where("chat_id = ?", chatID).
		Where("reason IN ('" + core.ReasonUsageLimit + "', '" + core.ReasonUsageLimitProbe + "')")
	if threadID != "" {
		q = q.Where("thread_id = ?", threadID)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func clearProviderBackoff(db orm.Context, now time.Time) *bun.UpdateQuery {
	now = now.UTC()
	return db.Bun().NewUpdate().Model((*ProviderBackoff)(nil)).
		Set(`waited_ms = waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(resume_at, ?0), ?0) - waiting_since)) * 1000)::BIGINT)`, now).
		Set("waiting_since = NULL").
		Set("resume_at = NULL").
		Set("attempt = 0").
		Set("updated_at = ?", now).
		Where("waiting_since IS NOT NULL")
}

// RecordUsageLimitWait marks the thread as parked until resumeAt on provider's usage limit
// (reason is core.ReasonUsageLimit or core.ReasonUsageLimitProbe).
func RecordUsageLimitWait(ctx context.Context, db orm.Context, chatID, threadID, provider, reason string, resumeAt, now time.Time) error {
	now = now.UTC()
	_, err := db.Bun().NewRaw(`
INSERT INTO provider_backoff (
    thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
    status_code, reason, provider, retries, waited_ms, updated_at
) VALUES (?0, ?1, ?2, ?3, 1, 0, 429, ?4, ?5, 1, 0, ?2)
ON CONFLICT (thread_id) DO UPDATE SET
    chat_id = EXCLUDED.chat_id,
    waiting_since = EXCLUDED.waiting_since,
    resume_at = EXCLUDED.resume_at,
    attempt = provider_backoff.attempt + 1,
    max_attempts = 0,
    status_code = 429,
    reason = EXCLUDED.reason,
    provider = EXCLUDED.provider,
    retries = provider_backoff.retries + 1,
    waited_ms = provider_backoff.waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(provider_backoff.resume_at, EXCLUDED.waiting_since), EXCLUDED.waiting_since)
        - provider_backoff.waiting_since)) * 1000)::BIGINT),
    updated_at = EXCLUDED.updated_at`,
		threadID, chatID, now, resumeAt.UTC(), reason, provider).Exec(ctx)
	return err
}

// ListChatProviderBackoff returns the backoff marker of every thread of the chat that has one,
// by thread id.
func ListChatProviderBackoff(ctx context.Context, db orm.Context, chatID string) (map[string]core.ProviderBackoff, error) {
	rows, err := ListProviderBackoff(ctx, db, orm.WhereEq(ProviderBackoffFieldChatID, chatID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]core.ProviderBackoff, len(rows))
	for _, r := range rows {
		b := core.ProviderBackoff{
			ThreadID: r.ThreadID, ChatID: r.ChatID, Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
			StatusCode: r.StatusCode, Reason: r.Reason, Retries: r.Retries, Provider: r.Provider,
			WaitedMs: r.WaitedMs, UpdatedAt: r.UpdatedAt,
		}
		if r.WaitingSince != nil {
			b.WaitingSince = *r.WaitingSince
		}
		if r.ResumeAt != nil {
			b.ResumeAt = *r.ResumeAt
		}
		out[b.ThreadID] = b
	}
	return out, nil
}

// ProviderBackoffExtra carries fields on ProviderBackoff that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProviderBackoff is
// embedded on ProviderBackoffExtra (see provider_backoff_orm.go), so a field added here is a field on
// every ProviderBackoff value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProviderBackoffExtra struct {
}

// listChatsHeldOnUsageLimitSQL: which of the chats ?0 have a thread parked on
// a usage-limit wait right now that began after ?1.
const listChatsHeldOnUsageLimitSQL = `
SELECT DISTINCT b.chat_id
FROM provider_backoff b
WHERE b.chat_id = ANY(?0::text[])
  AND b.waiting_since IS NOT NULL
  AND b.waiting_since > ?1
  AND b.reason IN ('` + core.ReasonUsageLimit + `', '` + core.ReasonUsageLimitProbe + `')
ORDER BY b.chat_id`

// ListChatsHeldOnUsageLimit returns the chats among chatIDs with a thread
// parked on a usage-limit wait that began after since
// (listChatsHeldOnUsageLimitSQL).
func ListChatsHeldOnUsageLimit(ctx context.Context, db orm.Context, chatIDs []string, since time.Time) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(listChatsHeldOnUsageLimitSQL, pgdialect.Array(chatIDs), since.UTC()).Scan(ctx, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// listCapacityWakeTargetsSQL: the root workflow ids of userID's chats whose run
// is live and may have a thread blocked on a provider — a thread parked on a
// usage-limit wait, a root paused (by itself on a provider failure, or by the
// user; the run tells the two apart), or a sub-agent run stopped on a failure.
// A capacity wake sent to one of these finds whatever is blocked; sent to a run
// with nothing blocked it is a no-op, so the filter only keeps the fan-out to
// the chats it can matter to.
const listCapacityWakeTargetsSQL = `
SELECT c.workflow_id
FROM chats c
JOIN workflows w ON w.id = c.workflow_id
WHERE c.user_id = ?0
  AND (w.state = 2 OR (w.state = 3 AND w.stop_reason = 3))
  AND (
        (w.state = 3 AND w.stop_reason = 3)
     OR EXISTS (
            SELECT 1 FROM provider_backoff b
            WHERE b.chat_id = c.id
              AND b.waiting_since IS NOT NULL
              AND b.reason IN ('` + core.ReasonUsageLimit + `', '` + core.ReasonUsageLimitProbe + `'))
     OR EXISTS (
            SELECT 1 FROM workflows s
            WHERE s.chat_id = c.id AND s.id <> c.workflow_id
              AND s.state = 3 AND s.stop_reason = 2)
  )
ORDER BY c.workflow_id`

// ListCapacityWakeTargets returns the root workflow ids of userID's chats a
// capacity wake can matter to (listCapacityWakeTargetsSQL).
func ListCapacityWakeTargets(ctx context.Context, db orm.Context, userID string) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(listCapacityWakeTargetsSQL, userID).Scan(ctx, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}
