// Copyright (c) 2026 Reliant Labs

package diskwatch

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Own caches: caches the daemon alone creates, and so prunes whatever the
// storage reaper's mode. A person's machine with the reaper off still never
// lets them grow without bound, the way the daemon's own GOTMPDIR roots are
// reclaimed there (reaper.go, runReaperOff).
//
// Each one brings its own policy in Prune; this file only decides when it
// runs: on the reaper's poll, every OwnCache.Interval, or every
// OwnCache.PressureInterval with urgent set while the volume holding its Path
// is under pressure or a command recently failed on it for want of space
// (ExplainNoSpace); and at once, urgent, from TrimCaches (the disk-pressure
// guard). It runs inside the storage single-flight, so it never overlaps a
// reaper pass or a user's clean-up.

const (
	// DefaultOwnCacheInterval is how often an own cache is pruned.
	DefaultOwnCacheInterval = time.Hour
	// DefaultOwnCachePressureInterval is how soon it repeats while its
	// volume is under pressure.
	DefaultOwnCachePressureInterval = 10 * time.Minute
	// ownCachePassBudget bounds one cache's prune.
	ownCachePassBudget = 15 * time.Minute
)

// OwnCache is a cache the daemon alone creates and prunes itself.
type OwnCache struct {
	// Name identifies it in the log (and replaces an earlier registration
	// of the same name).
	Name string
	// Path is where it lives: its volume's pressure makes a prune urgent.
	Path string
	// Interval is how often it is pruned; zero is DefaultOwnCacheInterval.
	Interval time.Duration
	// PressureInterval is how often while its volume is under pressure;
	// zero is DefaultOwnCachePressureInterval.
	PressureInterval time.Duration
	// Prune prunes it. urgent asks for the tighter budget: the disk is
	// filling. It never needs to return before ctx is done.
	Prune func(ctx context.Context, urgent bool)
}

// ownCacheEntry is a registered OwnCache and when it last ran.
type ownCacheEntry struct {
	cache OwnCache
	last  time.Time
}

// RegisterOwnCache adds c to the caches pruned on the reaper's poll. Call
// before RunReaper. Nil-safe.
func (w *Watcher) RegisterOwnCache(c OwnCache) {
	if w == nil || c.Name == "" || c.Prune == nil {
		return
	}
	if c.Interval <= 0 {
		c.Interval = DefaultOwnCacheInterval
	}
	if c.PressureInterval <= 0 {
		c.PressureInterval = DefaultOwnCachePressureInterval
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.ownCaches {
		if e.cache.Name == c.Name {
			e.cache = c
			return
		}
	}
	w.ownCaches = append(w.ownCaches, &ownCacheEntry{cache: c})
}

// OwnCaches names the registered own caches, in registration order.
func (w *Watcher) OwnCaches() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.ownCaches))
	for _, e := range w.ownCaches {
		out = append(out, e.cache.Name)
	}
	return out
}

// ownCachesTick prunes each own cache that is due, inside the storage
// single-flight, and reports whether it pruned any. A cache that is due while
// another pass holds the single-flight waits for the next poll.
func (w *Watcher) ownCachesTick(ctx context.Context) (ran bool) {
	if ctx.Err() != nil {
		return false
	}
	now := w.now()
	type due struct {
		e      *ownCacheEntry
		urgent bool
	}
	var list []due
	w.mu.Lock()
	entries := append([]*ownCacheEntry(nil), w.ownCaches...)
	w.mu.Unlock()
	for _, e := range entries {
		w.mu.Lock()
		c, last := e.cache, e.last
		w.mu.Unlock()
		urgent := w.pathUnderPressure(c.Path, c.PressureInterval)
		interval := c.Interval
		if urgent {
			interval = c.PressureInterval
		}
		if last.IsZero() || now.Sub(last) >= interval {
			list = append(list, due{e, urgent})
		}
	}
	if len(list) == 0 || !w.tryAcquireReaper() {
		return false
	}
	defer w.releaseReaper()
	for _, d := range list {
		if ctx.Err() != nil {
			break
		}
		trigger := "scheduled"
		if d.urgent {
			trigger = "pressure"
		}
		w.pruneOwnCache(ctx, d.e, d.urgent, trigger)
	}
	return true
}

// pruneOwnCaches prunes every own cache now. The caller holds the storage
// single-flight.
func (w *Watcher) pruneOwnCaches(ctx context.Context, urgent bool, trigger string) {
	w.mu.Lock()
	entries := append([]*ownCacheEntry(nil), w.ownCaches...)
	w.mu.Unlock()
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		w.pruneOwnCache(ctx, e, urgent, trigger)
	}
}

// pruneOwnCache runs one cache's prune and records when. A panic is logged,
// never taken down the reaper's goroutine.
func (w *Watcher) pruneOwnCache(ctx context.Context, e *ownCacheEntry, urgent bool, trigger string) {
	w.mu.Lock()
	e.last = w.now()
	c := e.cache
	w.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			logging.WarnContext(ctx, "[diskwatch] Own cache prune panicked", "cache", c.Name, "trigger", trigger, "panic", fmt.Sprint(r))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, ownCachePassBudget)
	defer cancel()
	c.Prune(ctx, urgent)
}

// pathUnderPressure reports whether the watched volume holding path is under
// pressure, or saw a command fail for want of space within the last window.
func (w *Watcher) pathUnderPressure(path string, window time.Duration) bool {
	if path == "" {
		return false
	}
	st, _, err := w.stat(path)
	if err != nil {
		return false
	}
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.valid {
		return false
	}
	for _, v := range w.vols {
		if !v.matches(st) {
			continue
		}
		if w.pressure[v.key] {
			return true
		}
		if at := w.noSpaceAt[v.key]; at != 0 && now.Sub(time.UnixMilli(at)) < window {
			return true
		}
	}
	return false
}
