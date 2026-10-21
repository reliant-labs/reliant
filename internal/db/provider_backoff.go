// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ProviderBackoff is one thread's provider backoff marker (core.ProviderBackoff);
// its statements are internal/store's (provider_backoff_repo_ext.go).
type ProviderBackoff = core.ProviderBackoff

// The reasons a workflow-level usage-limit wait writes.
const (
	ReasonUsageLimit      = core.ReasonUsageLimit
	ReasonUsageLimitProbe = core.ReasonUsageLimitProbe
)

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
	if err := store.RecordProviderBackoff(ctx, r.ORM(), chatID, threadID, attempt, maxAttempts, statusCode, reason, delay, now); err != nil {
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
	if err := store.ClearProviderBackoff(ctx, r.ORM(), threadID, now); err != nil {
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

	out, err := store.ListChatProviderBackoff(ctx, r.ORM(), chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to read provider backoff: %w", err)
	}
	return out, nil
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
	return r.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := store.RecordUsageLimitWait(txCtx, r.ORM(), chatID, threadID, provider, reason, resumeAt, now); err != nil {
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
	return r.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		n, err := store.ClearUsageLimitWait(txCtx, r.ORM(), chatID, threadID, now)
		if err != nil {
			return fmt.Errorf("failed to clear usage-limit wait: %w", err)
		}
		if n == 0 {
			return nil
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}
