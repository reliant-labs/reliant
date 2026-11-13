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

// An org a refusal was met in is woken when its policy moves to a newer
// version (a cap raised, a model allowed), and only then; it stays watched,
// so a later change wakes it again, and is forgotten after WatchFor.
func TestWatcherWakesTheOrgWhenItsPolicyChanges(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 3, doc: machines}, {version: 3, doc: machines}, {version: 4, doc: machines}, {version: 4, doc: machines}, {version: 5, doc: machines}}}
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
	w.Check(ctx) // v4 again: unchanged
	if woken.count() != 1 {
		t.Fatalf("woke again on an unchanged policy: %v", woken.orgs)
	}
	c.advance(CacheTTL)
	w.Check(ctx) // v5: a second loosening wakes it again
	if woken.count() != 2 {
		t.Fatalf("wakes = %v, want a second wake for v5", woken.orgs)
	}

	// Expiry: an org is forgotten after WatchFor with nothing owed.
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
	if !slices.Equal(woken.signaled, []string{"run-1", "run-2"}) || w.orgs["org-a"].changed {
		t.Fatalf("after recovery: signaled %v; want both runs and nothing owed", woken.signaled)
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
	if !slices.Equal(woken.signaled, []string{"run-1", "run-3", "run-2"}) || w.orgs["org-a"].changed {
		t.Fatalf("final delivery signaled %v; want only run-2 retried and nothing owed", woken.signaled)
	}
	if len(woken.orgs) != 1 {
		t.Fatalf("listed the org %d times; a partial delivery retries its missed runs, not the whole org", len(woken.orgs))
	}
}

// A refusal met while the policy cannot be read fresh (an outage) has no
// baseline: the stale answers during the outage, and the first fresh answer
// after it, wake nothing; that answer becomes the baseline, and only a real
// change from it wakes the org.
func TestWatcherLearnsItsBaselineAfterAnOutage(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 3, doc: machines}, {err: errDown}, {err: errDown}, {version: 3, doc: machines}, {version: 3, doc: machines}, {version: 4, doc: machines}}}
	r, c := newResolver(src)
	woken := &wakes{runs: map[string][]string{"org-a": {"run-1"}}}
	w := NewWatcher(r, woken)
	w.now = c.now
	ctx := context.Background()

	mustResolve(t, r, "org-a") // v3 cached
	c.advance(CacheTTL)
	w.Watch(ctx, "org-a") // the refusal is met in the outage: stale v3, no baseline
	if got := w.orgs["org-a"].version; got != -1 {
		t.Fatalf("baseline from a stale answer = %d, want unknown (-1)", got)
	}
	c.advance(retryAfter)
	w.Check(ctx) // still down: stale, nothing
	c.advance(retryAfter)
	w.Check(ctx) // recovered, fresh v3: the baseline, no wake
	if len(woken.signaled) != 0 || w.orgs["org-a"].version != 3 {
		t.Fatalf("recovery woke %v (baseline %d); want no wake and baseline v3", woken.signaled, w.orgs["org-a"].version)
	}
	c.advance(CacheTTL)
	w.Check(ctx) // v3 again: unchanged
	if len(woken.signaled) != 0 {
		t.Fatalf("an unchanged policy woke %v", woken.signaled)
	}
	c.advance(CacheTTL)
	w.Check(ctx) // v4: the real change
	if !slices.Equal(woken.signaled, []string{"run-1"}) {
		t.Fatalf("the real change woke %v; want run-1", woken.signaled)
	}
}
