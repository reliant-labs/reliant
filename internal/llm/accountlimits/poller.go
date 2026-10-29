// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	defaultPollEvery   = 30 * time.Second
	defaultLeaseBatch  = 50
	defaultLease       = 2 * time.Minute
	defaultConcurrency = 4
)

// leaser is the slice of the account store the Poller drives.
type leaser interface {
	LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error)
}

// Poller reads the usage API for accounts whose response headers do not cover
// them. Run it in every worker: accounts are leased with FOR UPDATE SKIP LOCKED
// (LeaseUsagePolls pushes usage_api_next_at forward by the lease), so replicas
// share the queue and no account is fetched twice. A worker that dies mid-poll
// loses nothing: its lease expires and another worker leases the account.
//
// Headers see only Reliant's traffic. The same subscription is often also
// spent from the user's own Claude Code or Codex CLI, invisibly to us, which is
// what least-usage routing needs to know about.
type Poller struct {
	Store   leaser
	Fetcher *Fetcher

	// Every is the lease interval (default 30 s). Batch bounds accounts per
	// lease (default 50). Lease is how long a leased account is ours (default
	// 2 min). Concurrency bounds simultaneous provider calls (default 4).
	Every       time.Duration
	Batch       int
	Lease       time.Duration
	Concurrency int
	Now         func() time.Time
}

// Run polls until ctx is done (a lifecyclekit.Runner).
func (p *Poller) Run(ctx context.Context) error {
	every := orDefault(p.Every, defaultPollEvery)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		// One trace per tick, so its lease query and provider calls nest under it.
		_ = observe.RunRoot(ctx, "accountlimits.poll", func(ctx context.Context) error {
			p.Tick(ctx)
			return nil
		})
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick leases the accounts due now and polls them, returning how many it handled.
func (p *Poller) Tick(ctx context.Context) int {
	if ctx.Err() != nil {
		return 0
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	ids, err := p.Store.LeaseUsagePolls(ctx, now, orDefaultInt(p.Batch, defaultLeaseBatch), orDefault(p.Lease, defaultLease))
	if err != nil {
		if ctx.Err() == nil {
			logging.WarnContext(ctx, "leasing usage polls failed", "error", err)
		}
		return 0
	}
	if len(ids) == 0 {
		return 0
	}

	sem := make(chan struct{}, orDefaultInt(p.Concurrency, defaultConcurrency))
	var wg sync.WaitGroup
	for _, id := range ids {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					logging.ErrorContext(ctx, "usage poll panicked", "account_id", id, "panic", r)
				}
			}()
			res := p.Fetcher.PollLeased(ctx, id)
			if res.Outcome == OutcomeSkipped && res.Err != nil && ctx.Err() == nil {
				logging.WarnContext(ctx, "usage poll skipped", "account_id", id, "error", res.Err)
			}
		}()
	}
	wg.Wait()
	return len(ids)
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func orDefaultInt(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}
