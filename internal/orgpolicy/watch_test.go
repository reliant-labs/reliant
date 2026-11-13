// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// wakes is a Waker over a fixed set of runs per org. listErr fails the
// listing; down are runs whose signal fails. It records every run signalled.
type wakes struct {
	mu       sync.Mutex
	orgs     []string
	runs     map[string][]string
	listErr  error
	down     map[string]bool
	signaled []string
}

func (w *wakes) WakeOrgChats(_ context.Context, orgID, _ string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.orgs = append(w.orgs, orgID)
	if w.listErr != nil {
		return nil, w.listErr
	}
	return w.signalLocked(w.runs[orgID]), nil
}

func (w *wakes) WakeRuns(_ context.Context, ids []string, _ string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.signalLocked(ids)
}

func (w *wakes) signalLocked(ids []string) (failed []string) {
	for _, id := range ids {
		if w.down[id] {
			failed = append(failed, id)
			continue
		}
		w.signaled = append(w.signaled, id)
	}
	return failed
}

func (w *wakes) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.orgs)
}

// An org a refusal was met in is woken once its policy version changes (a cap
// raised, a model allowed), and only then; it is forgotten after the wake and
// after WatchFor.
func TestWatcherWakesTheOrgWhenItsPolicyChanges(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 3, doc: machines}, {version: 3, doc: machines}, {version: 4, doc: machines}}}
	r, c := newResolver(src)
	woken := &wakes{}
	w := NewWatcher(r, woken)
	w.now = c.now
	ctx := context.Background()

	w.Watch(ctx, "org-a")
	c.advance(CacheTTL)
	w.Check(ctx) // still v3
	if woken.count() != 0 {
		t.Fatalf("woke on an unchanged policy: %v", woken.orgs)
	}
	c.advance(CacheTTL)
	w.Check(ctx) // v4
	if woken.count() != 1 || woken.orgs[0] != "org-a" {
		t.Fatalf("wakes = %v, want org-a once", woken.orgs)
	}
	c.advance(CacheTTL)
	w.Check(ctx)
	if woken.count() != 1 {
		t.Fatalf("woke again after the org was forgotten: %v", woken.orgs)
	}

	// Expiry: an org whose policy never changes is forgotten after WatchFor.
	w.Watch(ctx, "org-a")
	c.advance(WatchFor + time.Second)
	w.Check(ctx)
	w.mu.Lock()
	n := len(w.orgs)
	w.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d orgs still watched after WatchFor", n)
	}
}

// Nothing to watch through, or nothing to wake: no watcher, and every call is
// a no-op.
func TestNilWatcher(t *testing.T) {
	t.Parallel()
	if NewWatcher(nil, &wakes{}) != nil || NewWatcher(New(&scripted{replies: []reply{{}}}), nil) != nil {
		t.Fatal("a watcher without a resolver or a waker")
	}
	var w *Watcher
	w.Watch(context.Background(), "org-a")
	w.Check(context.Background())
	WatchRefused(context.Background(), "org-a") // no process watcher installed
}

// A wake a database outage stopped before listing a single chat keeps the org
// watched (the policy change is not forgotten), and a later tick, after the
// backoff, delivers it.
func TestWatcherRetriesAWakeTheListingMissed(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 3, doc: machines}, {version: 4, doc: machines}}}
	r, c := newResolver(src)
	woken := &wakes{runs: map[string][]string{"org-a": {"run-1", "run-2"}}, listErr: errors.New("db down")}
	w := NewWatcher(r, woken)
	w.now = c.now
	ctx := context.Background()

	w.Watch(ctx, "org-a")
	c.advance(CacheTTL)
	w.Check(ctx) // v4: the wake is owed, the listing fails
	if len(woken.signaled) != 0 || len(w.orgs) != 1 {
		t.Fatalf("after a failed listing: signaled %v, watching %d; want nothing signaled and org-a still watched", woken.signaled, len(w.orgs))
	}
	woken.mu.Lock()
	woken.listErr = nil
	woken.mu.Unlock()
	w.Check(ctx) // within the backoff: nothing yet
	if len(woken.signaled) != 0 {
		t.Fatalf("retried before the backoff: %v", woken.signaled)
	}
	c.advance(WatchInterval)
	w.Check(ctx)
	if !slices.Equal(woken.signaled, []string{"run-1", "run-2"}) || len(w.orgs) != 0 {
		t.Fatalf("after recovery: signaled %v, watching %d; want both runs and org-a forgotten", woken.signaled, len(w.orgs))
	}
}

// A partial delivery retries only the runs it missed, with growing backoff,
// until every one is reached; then the org is forgotten.
func TestWatcherRetriesOnlyTheRunsAPartialWakeMissed(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 3, doc: machines}, {version: 4, doc: machines}}}
	r, c := newResolver(src)
	woken := &wakes{runs: map[string][]string{"org-a": {"run-1", "run-2", "run-3"}}, down: map[string]bool{"run-2": true}}
	w := NewWatcher(r, woken)
	w.now = c.now
	ctx := context.Background()

	w.Watch(ctx, "org-a")
	c.advance(CacheTTL)
	w.Check(ctx)
	if !slices.Equal(woken.signaled, []string{"run-1", "run-3"}) {
		t.Fatalf("first delivery signaled %v", woken.signaled)
	}
	c.advance(WatchInterval)
	w.Check(ctx) // run-2 still down: second attempt, backoff doubles
	if !slices.Equal(woken.signaled, []string{"run-1", "run-3"}) || len(w.orgs) != 1 {
		t.Fatalf("second attempt signaled %v; want run-2 still owed", woken.signaled)
	}
	woken.mu.Lock()
	woken.down = nil
	woken.mu.Unlock()
	c.advance(WatchInterval)
	w.Check(ctx) // within the doubled backoff
	if len(woken.signaled) != 2 {
		t.Fatalf("retried within the backoff: %v", woken.signaled)
	}
	c.advance(WatchInterval)
	w.Check(ctx)
	if !slices.Equal(woken.signaled, []string{"run-1", "run-3", "run-2"}) || len(w.orgs) != 0 {
		t.Fatalf("final delivery signaled %v, watching %d; want only run-2 retried and org-a forgotten", woken.signaled, len(w.orgs))
	}
	if len(woken.orgs) != 1 {
		t.Fatalf("listed the org %d times; a partial delivery retries its missed runs, not the whole org", len(woken.orgs))
	}
}
