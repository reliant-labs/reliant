// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// LLMCallUsage is one model call's counts: tokens, model, provider, auth mode,
// outcome and latency. It has no field for prompt, reply, tool I/O or error
// text, and must not gain one (TestLLMUsageTablesHoldNoContent).
type LLMCallUsage = store.LLMCallUsageRecord

// LLMUsageDay is one rollup row: a UTC day's calls of one user, org, provider,
// model and auth mode.
type LLMUsageDay = store.LlmCallUsageDaily

// LLMCallUsageRetention is how long the per-call rows live. The daily rollup
// is kept; docs/help/privacy.mdx states this number.
const LLMCallUsageRetention = 90 * 24 * time.Hour

// llmUsagePruneBatch bounds one retention DELETE, so a backlog is worked off in
// short transactions rather than one long one.
const llmUsagePruneBatch = 5000

// RecordLLMCallUsage writes one call and its rollup. Idempotent by rec.ID (the
// activity's idempotency key): a Temporal retry of the same activity changes
// nothing, and a later attempt replaces the earlier one's row. Reports whether
// anything was written.
func (r *Repo) RecordLLMCallUsage(ctx context.Context, rec LLMCallUsage) (bool, error) {
	var written bool
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		written, err = store.RecordLLMCallUsage(ctx, r.ORM(), rec)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("record llm call usage: %w", err)
	}
	return written, nil
}

// PruneLLMCallUsage deletes the per-call rows older than LLMCallUsageRetention
// at now, in batches, and returns how many. The rollup keeps their totals.
func (r *Repo) PruneLLMCallUsage(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-LLMCallUsageRetention)
	var total int64
	for {
		n, err := store.PruneLLMCallUsage(orm.Detached(ctx), r.ORM(), cutoff, llmUsagePruneBatch)
		total += n
		if err != nil {
			return total, fmt.Errorf("prune llm call usage: %w", err)
		}
		if n < llmUsagePruneBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// ListLLMUsageDaily returns the user's rollup rows for the UTC days from..to
// inclusive, oldest first. This is what the usage API reads; it only ever
// returns the caller's own rows.
func (r *Repo) ListLLMUsageDaily(ctx context.Context, userID string, from, to time.Time) ([]*LLMUsageDay, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	rows, err := store.ListLLMUsageDaily(ctx, r.ORM(), userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list llm usage: %w", err)
	}
	return rows, nil
}
