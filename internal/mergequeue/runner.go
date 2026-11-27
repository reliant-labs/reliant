// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Enrolled lists the repositories whose mode is not off: *db.Repo's
// ListEnrolledPipelineRepos.
type Enrolled interface {
	ListEnrolledPipelineRepos(ctx context.Context) ([]*core.PipelineRepo, error)
}

// Run drives the queue of every enrolled repository until ctx ends: it looks
// for newly enrolled repositories every interval and drives each one
// ([Scheduler.Drive]) on its own goroutine. Every worker runs it; the leader
// lock makes exactly one of them drive each repository at a time.
func (s *Scheduler) Run(ctx context.Context, repos Enrolled, every time.Duration) error {
	if every <= 0 {
		every = DefaultTickInterval
	}
	var (
		mu      sync.Mutex
		driving = map[string]bool{}
		wg      sync.WaitGroup
	)
	defer wg.Wait()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		list, err := repos.ListEnrolledPipelineRepos(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "merge queue: list enrolled repositories", "error", err)
		}
		for _, r := range list {
			mu.Lock()
			if driving[r.ID] {
				mu.Unlock()
				continue
			}
			driving[r.ID] = true
			mu.Unlock()
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				_ = s.Drive(ctx, id, every)
				mu.Lock()
				delete(driving, id)
				mu.Unlock()
			}(r.ID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// DefaultTickInterval is how often a worker ticks each repository.
const DefaultTickInterval = 30 * time.Second
