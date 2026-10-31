// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The worktree root holds hundreds of checkouts and tens of millions of
// files, most of them in node_modules. One walk of it under dirBudget and
// dirMaxEntries stopped after a few GB, so a 200 GB root was reported as
// "Workspaces 4 GB" and the rest landed in "Other". It is measured instead one
// worktree at a time, each remembered across scans: every scan re-measures
// the worktrees it has not seen, then the ones measured longest ago, for as
// long as its budget lasts, and reports the sum of what it knows. A worktree
// not measured yet is estimated at the mean of those that were, and the total
// is then marked approximate, so the root is never undercounted to nothing
// while the cache fills.

const (
	// worktreeScanBudget bounds the walking one scan spends on the root.
	worktreeScanBudget = 30 * time.Second
	// worktreeUnitMaxEntries bounds one worktree's walk; a walk cut short
	// records a lower bound and the worktree is re-measured first next time.
	worktreeUnitMaxEntries = 5_000_000
	// worktreeUnitRefresh is how old a remembered size may get before the
	// worktree is measured again (when the budget reaches it).
	worktreeUnitRefresh = 30 * time.Minute
)

// unitSize is one worktree's remembered size.
type unitSize struct {
	bytes uint64
	at    time.Time
	// complete is false when the walk was cut short: bytes is a lower bound.
	complete bool
}

// worktreeSizes remembers each worktree's size across scans.
type worktreeSizes struct {
	mu    sync.Mutex
	units map[string]unitSize
	// budget overrides worktreeScanBudget (tests).
	budget time.Duration
}

// worktreeUnits lists the directories that are one worktree each — the
// root's grandchildren (<root>/<project>/<worktree>, and .reclaim/<id>) —
// and sums the regular files that sit above that level. Symlinks are never
// followed.
func worktreeUnits(root string) (units []string, loose uint64) {
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil, 0
	}
	for _, p := range projects {
		pp := filepath.Join(root, p.Name())
		if !p.IsDir() {
			loose += regularSize(p)
			continue
		}
		children, err := os.ReadDir(pp)
		if err != nil {
			continue
		}
		for _, c := range children {
			if c.IsDir() {
				units = append(units, filepath.Join(pp, c.Name()))
			} else {
				loose += regularSize(c)
			}
		}
	}
	return units, loose
}

func regularSize(d os.DirEntry) uint64 {
	if !d.Type().IsRegular() {
		return 0
	}
	info, err := d.Info()
	if err != nil {
		return 0
	}
	return allocatedBytes(info)
}

// measure reports the worktree root's size as of now: exact when every
// worktree has a complete measurement, otherwise approximate (lower bounds
// for walks cut short, the mean for worktrees not measured yet).
func (s *worktreeSizes) measure(ctx context.Context, root string, now time.Time) dirMeasure {
	units, loose := worktreeUnits(root)
	budget := s.budget
	if budget <= 0 {
		budget = worktreeScanBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	s.mu.Lock()
	if s.units == nil {
		s.units = map[string]unitSize{}
	}
	present := make(map[string]bool, len(units))
	for _, u := range units {
		present[u] = true
	}
	for u := range s.units {
		if !present[u] {
			delete(s.units, u) // removed since: its disk is free
		}
	}
	// Never measured, then cut short, then oldest; fresh ones are skipped.
	type due struct {
		path string
		rank int
		at   time.Time
	}
	var queue []due
	for _, u := range units {
		c, ok := s.units[u]
		switch {
		case !ok:
			queue = append(queue, due{u, 0, time.Time{}})
		case !c.complete:
			queue = append(queue, due{u, 1, c.at})
		case now.Sub(c.at) >= worktreeUnitRefresh:
			queue = append(queue, due{u, 2, c.at})
		}
	}
	s.mu.Unlock()
	sort.SliceStable(queue, func(i, j int) bool {
		if queue[i].rank != queue[j].rank {
			return queue[i].rank < queue[j].rank
		}
		return queue[i].at.Before(queue[j].at)
	})

	for _, q := range queue {
		remaining := time.Until(deadlineOf(ctx))
		if ctx.Err() != nil || remaining <= 0 {
			break
		}
		dm := measureDirWithin(ctx, q.path, nil, false, time.Time{}, remaining, worktreeUnitMaxEntries)
		s.mu.Lock()
		prev, had := s.units[q.path]
		// A walk cut short is a lower bound; never let it shrink a complete
		// earlier answer that is merely old.
		if !dm.approx || !had || !prev.complete || dm.bytes > prev.bytes {
			s.units[q.path] = unitSize{bytes: dm.bytes, at: now, complete: !dm.approx}
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	m := dirMeasure{bytes: loose}
	var known uint64
	var measured, unmeasured int
	for _, u := range units {
		c, ok := s.units[u]
		if !ok {
			unmeasured++
			continue
		}
		known += c.bytes
		measured++
		if !c.complete {
			m.approx = true
		}
	}
	m.bytes += known
	if unmeasured > 0 {
		m.approx = true
		if measured > 0 {
			m.bytes += known / uint64(measured) * uint64(unmeasured)
		}
	}
	return m
}

// deadlineOf is ctx's deadline; ctx always has one here.
func deadlineOf(ctx context.Context) time.Time {
	dl, _ := ctx.Deadline()
	return dl
}
