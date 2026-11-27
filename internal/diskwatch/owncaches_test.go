// Copyright (c) 2026 Reliant Labs

package diskwatch

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// ownCacheRecorder is an own cache that records each prune's urgency.
type ownCacheRecorder struct {
	mu     sync.Mutex
	urgent []bool
}

func (r *ownCacheRecorder) register(w *Watcher, path string) {
	w.RegisterOwnCache(OwnCache{Name: "jobs", Path: path, Prune: func(_ context.Context, urgent bool) {
		r.mu.Lock()
		r.urgent = append(r.urgent, urgent)
		r.mu.Unlock()
	}})
}

func (r *ownCacheRecorder) calls() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.urgent...)
}

func TestOwnCaches_PrunedOnTheirIntervalAndUrgentlyUnderPressure(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	var r ownCacheRecorder
	r.register(h.w, h.cache)
	if got := h.w.OwnCaches(); !slices.Equal(got, []string{"jobs"}) {
		t.Fatalf("OwnCaches() = %v", got)
	}
	ctx := context.Background()

	if !h.w.ownCachesTick(ctx) || !slices.Equal(r.calls(), []bool{false}) {
		t.Fatalf("first tick: want one scheduled prune, got %v", r.calls())
	}
	h.advance(30 * time.Minute)
	if h.w.ownCachesTick(ctx) {
		t.Fatal("30m after a prune, inside the hourly interval: must not run")
	}
	h.advance(31 * time.Minute)
	if !h.w.ownCachesTick(ctx) || !slices.Equal(r.calls(), []bool{false, false}) {
		t.Fatalf("61m after: want a second scheduled prune, got %v", r.calls())
	}

	// Its volume fills: the pressure interval, urgent.
	h.setFree(0)
	waitScanned(t, h.w)
	h.advance(5 * time.Minute)
	if h.w.ownCachesTick(ctx) {
		t.Fatal("5m after, under pressure: inside the 10m pressure interval, must not run")
	}
	h.advance(5 * time.Minute)
	if !h.w.ownCachesTick(ctx) || !slices.Equal(r.calls(), []bool{false, false, true}) {
		t.Fatalf("10m after, under pressure: want an urgent prune, got %v", r.calls())
	}
}

func TestOwnCaches_ANoSpaceFailureOnTheirVolumeMakesThemUrgent(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	var r ownCacheRecorder
	r.register(h.w, h.cache)
	h.w.ownCachesTick(context.Background())
	h.advance(10 * time.Minute)

	st, _, err := h.w.stat(h.cache)
	if err != nil {
		t.Fatal(err)
	}
	h.w.mu.Lock()
	for _, v := range h.w.vols {
		if v.matches(st) {
			h.w.noSpaceAt[v.key] = h.now.UnixMilli()
		}
	}
	h.w.mu.Unlock()
	if !h.w.ownCachesTick(context.Background()) || !slices.Equal(r.calls(), []bool{false, true}) {
		t.Fatalf("a command just failed for want of space on its volume: want an urgent prune, got %v", r.calls())
	}
}

func TestOwnCaches_RunWithTheReaperOff(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.w.ConfigureReaper(ReaperConfig{Enabled: false, PolicyPath: h.policy})
	var r ownCacheRecorder
	r.register(h.w, h.cache)

	if !h.w.reaperOffTick(context.Background()) || !slices.Equal(r.calls(), []bool{false}) {
		t.Fatalf("the daemon's own caches are pruned whatever the reaper's mode: got %v", r.calls())
	}
	if h.runs() != 0 {
		t.Fatalf("the reaper ran with its schedule off: runs=%d", h.runs())
	}
}

func TestOwnCaches_TrimCachesPrunesThemUrgently(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	var r ownCacheRecorder
	r.register(h.w, h.cache)

	h.w.TrimCaches(context.Background(), true)

	if !slices.Equal(r.calls(), []bool{true}) {
		t.Fatalf("the disk guard's trim: want one urgent prune, got %v", r.calls())
	}
}

func TestOwnCaches_WaitForAPassAlreadyRunning(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	var r ownCacheRecorder
	r.register(h.w, h.cache)
	if !h.w.tryAcquireReaper() {
		t.Fatal("single-flight busy")
	}
	ran := h.w.ownCachesTick(context.Background())
	h.w.releaseReaper()
	if ran || len(r.calls()) != 0 {
		t.Fatalf("a prune ran beside another storage pass: %v", r.calls())
	}
	if !h.w.ownCachesTick(context.Background()) || len(r.calls()) != 1 {
		t.Fatalf("the next poll: want the prune, got %v", r.calls())
	}
}
