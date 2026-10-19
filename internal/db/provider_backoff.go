// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ProviderBackoff is one thread's LLM-provider backoff state: whether it is
// parked in a provider's retry ladder right now, and how much of its life it has
// spent there.
//
// This is the second kind of durable wait marker in the schema. The first —
// questions, approvals, pause — parks a run on a HUMAN, and every one of those
// park points writes its marker before parking so `workflow ps` can tell waiting
// from wedged. A provider retry ladder parks a run just as completely, but it
// runs inside a single Temporal activity attempt, so it wrote nothing at all:
// no message, no step execution, no status change. A supervisor reading any
// surface saw a unit that looked like it was thinking.
type ProviderBackoff struct {
	ThreadID string
	ChatID   string
	// WaitingSince is when the current sleep began; zero when the thread is not
	// currently parked.
	WaitingSince time.Time
	// ResumeAt is when the current sleep is due to end; zero when not parked.
	ResumeAt time.Time
	// Attempt is the request number that failed and triggered the current wait.
	Attempt int64
	// MaxAttempts is the driver's retry ceiling.
	MaxAttempts int64
	// StatusCode is the provider HTTP status that triggered the wait (429, 503…).
	StatusCode int64
	// Reason is the driver's retry-decision reason ("http_429", …).
	Reason string
	// Retries is the cumulative number of provider retries on this thread.
	Retries int64
	// Provider names who imposed a usage limit ("Anthropic"), for the reasons
	// below; empty for the driver's in-activity ladder.
	Provider string
	// WaitedMs is the cumulative time this thread has spent asleep in provider
	// backoff. It survives the run so post-hoc forensics can answer what fraction
	// of a unit's life went to rate limiting.
	WaitedMs  int64
	UpdatedAt time.Time
}

// Reasons a workflow-level usage-limit wait writes (a driver's retry ladder
// writes its own, "http_429" and friends). A usage window clears in hours, so
// the run parks on a durable workflow timer instead of sleeping inside an
// activity.
const (
	// ReasonUsageLimit: the provider named when the window resets; ResumeAt is
	// that time plus a margin.
	ReasonUsageLimit = "usage_limit"
	// ReasonUsageLimitProbe: the provider named no reset, so ResumeAt is the
	// next rung of the probe ladder.
	ReasonUsageLimitProbe = "usage_limit_probe"
)

// IsUsageLimit reports whether the marker is a workflow-level usage-limit wait.
func (b ProviderBackoff) IsUsageLimit() bool {
	return b.Reason == ReasonUsageLimit || b.Reason == ReasonUsageLimitProbe
}

// Waiting reports whether the thread is parked in provider backoff right now.
func (b ProviderBackoff) Waiting() bool { return !b.WaitingSince.IsZero() }

// RecordProviderBackoff marks a thread as parked in provider backoff and rolls
// the previous wait, if any, into the cumulative total.
//
// Called BEFORE the driver sleeps, which is the only ordering that makes the
// marker useful: one written afterwards cannot distinguish a run that is waiting
// right now from one that is working.
//
// The cumulative total charges only the portion of the previous wait that was
// actually slept — LEAST(now, resume_at) - waiting_since — so a wait cut short by
// cancellation is not counted as if it had run to completion.
func (r *Repo) RecordProviderBackoff(ctx context.Context, chatID, threadID string, attempt, maxAttempts, statusCode int, reason string, delay time.Duration, now time.Time) error {
	if chatID == "" || threadID == "" {
		return fmt.Errorf("chat ID and thread ID are required to record provider backoff")
	}
	now = now.UTC()
	query := r.bindQuery(`
INSERT INTO provider_backoff (
    thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
    status_code, reason, retries, waited_ms, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)
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
    updated_at = EXCLUDED.updated_at`)

	_, err := r.DB.DB(ctx).ExecContext(ctx, query,
		threadID, chatID, now, now.Add(delay), attempt, maxAttempts, statusCode, reason, now)
	if err != nil {
		return fmt.Errorf("failed to record provider backoff: %w", err)
	}
	return nil
}

// ClearProviderBackoff releases a thread from provider backoff, charging the
// slept portion of the open wait to the cumulative total. The row is kept, not
// deleted: the cumulative columns are what `reliant-dev workflow analyze` reads to say where
// a finished run's time went.
//
// A no-op when the thread has no row, so the caller can clear unconditionally.
func (r *Repo) ClearProviderBackoff(ctx context.Context, threadID string, now time.Time) error {
	if threadID == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	now = now.UTC()
	query := r.bindQuery(`
UPDATE provider_backoff SET
    waited_ms = waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(resume_at, ?), ?) - waiting_since)) * 1000)::BIGINT),
    waiting_since = NULL,
    resume_at = NULL,
    attempt = 0,
    updated_at = ?
WHERE thread_id = ? AND waiting_since IS NOT NULL`)

	_, err := r.DB.DB(ctx).ExecContext(ctx, query, now, now, now, threadID)
	if err != nil {
		return fmt.Errorf("failed to clear provider backoff: %w", err)
	}
	return nil
}

// ProviderBackoffByChat returns every thread of a chat that has a provider
// backoff row, keyed by thread id. Read-only: one SELECT.
func (r *Repo) ProviderBackoffByChat(ctx context.Context, chatID string) (map[string]ProviderBackoff, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	query := r.bindQuery(`
SELECT thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
       status_code, reason, retries, waited_ms, updated_at, provider
FROM provider_backoff WHERE chat_id = ?`)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to read provider backoff: %w", err)
	}
	defer rows.Close()

	out := map[string]ProviderBackoff{}
	for rows.Next() {
		var b ProviderBackoff
		var since, resume sql.NullTime
		if err := rows.Scan(&b.ThreadID, &b.ChatID, &since, &resume, &b.Attempt,
			&b.MaxAttempts, &b.StatusCode, &b.Reason, &b.Retries, &b.WaitedMs, &b.UpdatedAt, &b.Provider); err != nil {
			return nil, fmt.Errorf("failed to scan provider backoff: %w", err)
		}
		if since.Valid {
			b.WaitingSince = since.Time
		}
		if resume.Valid {
			b.ResumeAt = resume.Time
		}
		out[b.ThreadID] = b
	}
	return out, rows.Err()
}

// RecordUsageLimitWait marks a thread as parked until a provider's usage limit
// resets, and tells the chat's watchers when the thread is the chat's root run.
//
// Called by the workflow BEFORE it parks on its timer, for the same reason
// RecordProviderBackoff is called before a driver sleeps: a marker written
// afterwards cannot tell a run that is waiting from one that is wedged.
// probing is true when the provider named no reset and resumeAt is only the
// next rung of the probe ladder.
func (r *Repo) RecordUsageLimitWait(ctx context.Context, chatID, threadID, provider string, probing bool, resumeAt, now time.Time) error {
	if chatID == "" || threadID == "" {
		return fmt.Errorf("chat ID and thread ID are required to record a usage-limit wait")
	}
	reason := ReasonUsageLimit
	if probing {
		reason = ReasonUsageLimitProbe
	}
	now = now.UTC()
	query := r.bindQuery(`
INSERT INTO provider_backoff (
    thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
    status_code, reason, provider, retries, waited_ms, updated_at
) VALUES (?, ?, ?, ?, 1, 0, 429, ?, ?, 1, 0, ?)
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
    updated_at = EXCLUDED.updated_at`)
	return r.RunTx(ctx, func(txCtx context.Context) error {
		if _, err := r.DB.DB(txCtx).ExecContext(txCtx, query,
			threadID, chatID, now, resumeAt.UTC(), reason, provider, now); err != nil {
			return fmt.Errorf("failed to record usage-limit wait: %w", err)
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}

// ClearUsageLimitWait releases a thread from a usage-limit wait, charging the
// slept portion to the cumulative total. An empty threadID releases every
// thread of the chat, which is what a stop needs: a terminated run executes no
// code to clear its own markers. A no-op when nothing is waiting.
func (r *Repo) ClearUsageLimitWait(ctx context.Context, chatID, threadID string, now time.Time) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	now = now.UTC()
	query := `
UPDATE provider_backoff SET
    waited_ms = waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(resume_at, ?), ?) - waiting_since)) * 1000)::BIGINT),
    waiting_since = NULL,
    resume_at = NULL,
    attempt = 0,
    updated_at = ?
WHERE chat_id = ? AND waiting_since IS NOT NULL
  AND reason IN ('usage_limit', 'usage_limit_probe')`
	args := []interface{}{now, now, now, chatID}
	if threadID != "" {
		query += ` AND thread_id = ?`
		args = append(args, threadID)
	}
	query = r.bindQuery(query)
	return r.RunTx(ctx, func(txCtx context.Context) error {
		res, err := r.DB.DB(txCtx).ExecContext(txCtx, query, args...)
		if err != nil {
			return fmt.Errorf("failed to clear usage-limit wait: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}
