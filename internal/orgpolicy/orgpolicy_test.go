// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
)

// scripted answers each call with the next reply, repeating the last one.
type scripted struct {
	mu      sync.Mutex
	replies []reply
	calls   int
	// gate, when set, blocks every call until it is closed.
	gate chan struct{}
}

type reply struct {
	version int64
	doc     *controlplanev1.OrgPolicy
	err     error
}

func (s *scripted) ResolveOrgPolicy(ctx context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return accesstokenclient.OrgPolicy{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	r := s.replies[i]
	return accesstokenclient.OrgPolicy{Version: r.version, Policy: r.doc}, r.err
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// clock is a fake clock the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newResolver(src Source) (*Resolver, *clock) {
	c := &clock{t: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	r := New(src)
	r.now = c.now
	return r, c
}

var (
	errDown  = errors.New("connection refused")
	machines = &controlplanev1.OrgPolicy{Machines: &controlplanev1.MachinesPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, ForceCredentialsNone: true,
	}}
)

func mustResolve(t *testing.T, r *Resolver, orgID string) Policy {
	t.Helper()
	p, err := r.Resolve(context.Background(), orgID)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", orgID, err)
	}
	return p
}

// An answer is trusted for CacheTTL and fetched again after it, on a fake
// clock; every answer carries its version.
func TestCacheTTL(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 3, doc: machines}, {version: 4, doc: machines}}}
	r, c := newResolver(src)
	if p := mustResolve(t, r, "org-a"); p.Version != 3 || p.None() || p.Stale || p.OrgID != "org-a" {
		t.Fatalf("first answer = %+v", p)
	}
	c.advance(CacheTTL - time.Second)
	if p := mustResolve(t, r, "org-a"); p.Version != 3 || src.count() != 1 {
		t.Fatalf("within the TTL: %+v after %d calls, want the cached v3 after 1", p, src.count())
	}
	c.advance(time.Second)
	if p := mustResolve(t, r, "org-a"); p.Version != 4 || src.count() != 2 {
		t.Fatalf("at the TTL: %+v after %d calls, want v4 after 2", p, src.count())
	}
}

// No policy is a successful answer, cached like any other; a nil Resolver (no
// control plane), work outside any org, and a control plane that predates
// the rpc all answer it too.
func TestNoPolicyIsAnAnswer(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 0}}}
	r, _ := newResolver(src)
	for range 3 {
		if p := mustResolve(t, r, "org-a"); !p.None() || p.Version != 0 || p.Doc == nil {
			t.Fatalf("no policy = %+v", p)
		}
	}
	if src.count() != 1 {
		t.Errorf("no policy was not cached: %d calls", src.count())
	}

	var nilResolver *Resolver
	if p := mustResolve(t, nilResolver, "org-a"); !p.None() {
		t.Errorf("nil Resolver = %+v", p)
	}
	if New(nil) != nil {
		t.Error("New(nil) is not the no-policy Resolver")
	}
	if p := mustResolve(t, r, ""); !p.None() || src.count() != 1 {
		t.Errorf("no org = %+v after %d calls", p, src.count())
	}

	old, _ := newResolver(&scripted{replies: []reply{{err: fmt.Errorf("%w: ResolveOrgPolicy: unimplemented", accesstokenclient.ErrUnsupported)}}})
	if p := mustResolve(t, old, "org-a"); !p.None() || p.Version != 0 {
		t.Errorf("old control plane = %+v, want no policy", p)
	}
}

// Within Grace of the last good answer an outage serves it, Stale; the next
// fetch waits retryAfter; beyond Grace the org's work fails closed, retryably.
func TestOutageWithinAndBeyondGrace(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 7, doc: machines}, {err: errDown}}}
	r, c := newResolver(src)
	mustResolve(t, r, "org-a")

	c.advance(CacheTTL)
	p := mustResolve(t, r, "org-a")
	if p.Version != 7 || !p.Stale || src.count() != 2 {
		t.Fatalf("outage within grace = %+v after %d calls, want stale v7 after 2", p, src.count())
	}
	c.advance(retryAfter - time.Second)
	if p := mustResolve(t, r, "org-a"); !p.Stale || src.count() != 2 {
		t.Fatalf("within the retry wait: %+v after %d calls, want no new call", p, src.count())
	}
	c.advance(time.Second)
	mustResolve(t, r, "org-a")
	if src.count() != 3 {
		t.Fatalf("after the retry wait: %d calls, want 3", src.count())
	}

	c.advance(Grace) // well past Grace of the v7 fetch
	_, err := r.Resolve(context.Background(), "org-a")
	if !IsUnavailable(err) || connect.CodeOf(err) != connect.CodeUnavailable || reasonOf(err) != ReasonUnavailable {
		t.Fatalf("beyond grace: err = %v (code %v, reason %q), want Unavailable %q", err, connect.CodeOf(err), reasonOf(err), ReasonUnavailable)
	}

	// Recovery: the next answer after the retry wait is served again.
	src.mu.Lock()
	src.replies = append(src.replies, reply{version: 8, doc: machines})
	src.calls = len(src.replies) - 1
	src.mu.Unlock()
	c.advance(retryAfter)
	if p := mustResolve(t, r, "org-a"); p.Version != 8 || p.Stale {
		t.Fatalf("after recovery = %+v, want fresh v8", p)
	}
}

// An org never answered for fails closed at once; "no policy" is still a
// policy within Grace (an outage never turns it into "unknown" early).
func TestOutageWithNothingHeld(t *testing.T) {
	t.Parallel()
	r, c := newResolver(&scripted{replies: []reply{{err: errDown}}})
	if _, err := r.Resolve(context.Background(), "org-new"); !IsUnavailable(err) {
		t.Fatalf("never answered: err = %v, want unavailable", err)
	}
	c.advance(time.Second) // within the retry wait: the refusal repeats, no call
	if _, err := r.Resolve(context.Background(), "org-new"); !IsUnavailable(err) {
		t.Fatalf("within the retry wait: err = %v", err)
	}

	src := &scripted{replies: []reply{{version: 0}, {err: errDown}}}
	r2, c2 := newResolver(src)
	mustResolve(t, r2, "org-a")
	c2.advance(Grace - time.Second)
	if p := mustResolve(t, r2, "org-a"); !p.None() || !p.Stale {
		t.Fatalf("no policy within grace = %+v, want stale no-policy", p)
	}
	c2.advance(time.Second + retryAfter)
	if _, err := r2.Resolve(context.Background(), "org-a"); !IsUnavailable(err) {
		t.Fatalf("no policy beyond grace: err = %v, want unavailable", err)
	}
}

// Versions only move forward: an older answer never replaces a newer one.
func TestVersionIsMonotonic(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 5, doc: machines}, {version: 3}, {version: 6}}}
	r, c := newResolver(src)
	want := []int64{5, 5, 6}
	for i, w := range want {
		if i > 0 {
			c.advance(CacheTTL)
		}
		if p := mustResolve(t, r, "org-a"); p.Version != w {
			t.Fatalf("answer %d = v%d, want v%d", i, p.Version, w)
		}
	}
}

// Concurrent callers for one org share one fetch.
func TestOneFetchPerOrg(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 2, doc: machines}}, gate: make(chan struct{})}
	r, _ := newResolver(src)
	var (
		wg  sync.WaitGroup
		bad atomic.Int32
	)
	// Whether a caller arrives while the fetch is held or after it settled,
	// it is answered without a second fetch.
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, err := r.Resolve(context.Background(), "org-a"); err != nil || p.Version != 2 {
				bad.Add(1)
			}
		}()
	}
	close(src.gate)
	wg.Wait()
	if bad.Load() != 0 || src.count() != 1 {
		t.Fatalf("%d bad answers, %d fetches; want 0 and 1", bad.Load(), src.count())
	}
}

// A caller that gives up is not an outage: nothing is recorded against the
// org, and the next caller fetches.
func TestCallerCancelIsNotAnOutage(t *testing.T) {
	t.Parallel()
	src := &scripted{replies: []reply{{version: 1, doc: machines}}, gate: make(chan struct{})}
	r, _ := newResolver(src)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Resolve(ctx, "org-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: err = %v", err)
	}
	close(src.gate)
	if p := mustResolve(t, r, "org-a"); p.Version != 1 {
		t.Fatalf("next caller = %+v", p)
	}
}

// The Resolver installed on a context wins over the process's, which answers
// a context that carries none; with neither there is no policy.
func TestOnTheContext(t *testing.T) {
	// Not parallel: it sets the process resolver.
	t.Cleanup(func() { SetProcessResolver(nil) })
	onCtx, _ := newResolver(&scripted{replies: []reply{{version: 1, doc: machines}}})
	proc, _ := newResolver(&scripted{replies: []reply{{version: 9, doc: machines}}})

	if p, err := For(context.Background(), "org-a"); err != nil || !p.None() {
		t.Fatalf("no resolver anywhere = %+v, %v", p, err)
	}
	SetProcessResolver(proc)
	if p, _ := For(context.Background(), "org-a"); p.Version != 9 {
		t.Fatalf("process resolver = %+v", p)
	}
	if p, _ := For(WithResolver(context.Background(), onCtx), "org-a"); p.Version != 1 {
		t.Fatalf("context resolver = %+v", p)
	}
	if ResolverFrom(WithResolver(context.Background(), nil)) != proc {
		t.Error("a nil resolver on the context did not fall back to the process's")
	}
}

func reasonOf(err error) string {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return ""
	}
	return ce.Meta().Get(svcerr.ReasonHeader)
}
