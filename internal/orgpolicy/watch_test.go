// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"sync"
	"testing"
	"time"
)

type wakes struct {
	mu   sync.Mutex
	orgs []string
}

func (w *wakes) WakeOrgChats(_ context.Context, orgID, _ string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.orgs = append(w.orgs, orgID)
	return 1
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
