// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package llmusage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Retention is how long the per-call rows live. The daily rollup is kept;
// docs/help/privacy.mdx states this number.
const Retention = 90 * 24 * time.Hour

// pruneBatch bounds one retention DELETE, so a backlog is worked off in short
// transactions rather than one long one.
const pruneBatch = 5000

// Deps holds dependencies for the llmusage package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Ledger holds the per-call rows and their daily rollups.
	Ledger Ledger
	// Tools holds the tool calls' rows (agent-analytics.md §3.5).
	Tools ToolLedger
	// Tx opens the transaction a call's row and rollup are written in.
	Tx TxRunner
}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new llmusage service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Ledger == nil || deps.Tools == nil || deps.Tx == nil {
		return nil, errors.New("llmusage: Deps.Ledger, Deps.Tools and Deps.Tx are required")
	}
	return &service{deps: deps}, nil
}

// RecordToolCallUsage writes one tool call's record, (rec.ID, rec.Attempt):
// a repeated write of the same attempt changes nothing, and a user whose
// account was purged is refused (false, nil). Reports whether anything was
// written.
func (s *service) RecordToolCallUsage(ctx context.Context, rec store.ToolCallUsageRecord) (bool, error) {
	var written bool
	err := s.deps.Tx.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		written, err = s.deps.Tools.RecordToolCallUsage(ctx, rec)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("record tool call usage: %w", err)
	}
	return written, nil
}

// PruneToolCallUsage deletes the tool calls' rows older than Retention at
// now, in batches, and returns how many.
func (s *service) PruneToolCallUsage(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-Retention)
	var total int64
	for {
		n, err := s.deps.Tools.PruneToolCallUsage(orm.Detached(ctx), cutoff, pruneBatch)
		total += n
		if err != nil {
			return total, fmt.Errorf("prune tool call usage: %w", err)
		}
		if n < pruneBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// RecordLLMCallUsage writes one provider request and its rollup. A request is
// (rec.ID, rec.Attempt): each Temporal attempt is its own call and is counted,
// and only a repeated write of the same attempt changes nothing. A user whose
// account was purged is refused (false, nil). Reports whether anything was
// written.
func (s *service) RecordLLMCallUsage(ctx context.Context, rec store.LLMCallUsageRecord) (bool, error) {
	var written bool
	err := s.deps.Tx.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		written, err = s.deps.Ledger.RecordLLMCallUsage(ctx, rec)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("record llm call usage: %w", err)
	}
	return written, nil
}

// PruneLLMCallUsage deletes the per-call rows older than Retention at now, in
// batches, and returns how many. The rollup keeps their totals.
func (s *service) PruneLLMCallUsage(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-Retention)
	var total int64
	for {
		n, err := s.deps.Ledger.PruneLLMCallUsage(orm.Detached(ctx), cutoff, pruneBatch)
		total += n
		if err != nil {
			return total, fmt.Errorf("prune llm call usage: %w", err)
		}
		if n < pruneBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// PruneLLMUsagePurgeFences deletes the account-deletion fences old enough that
// no call in flight at deletion can still be running.
func (s *service) PruneLLMUsagePurgeFences(ctx context.Context, now time.Time) (int64, error) {
	n, err := s.deps.Ledger.PruneLLMUsagePurgeFences(orm.Detached(ctx), now)
	if err != nil {
		return 0, fmt.Errorf("prune llm usage purge fences: %w", err)
	}
	return n, nil
}
