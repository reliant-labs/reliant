// Copyright (c) 2025 Reliant Labs

// Package catalogcache caches a provider account's model catalog (Codex's
// /codex/models, Copilot's /models) so that resolving a model never waits on
// the provider when an answer is already known.
//
// Resolution asks for the catalog on the request path: SendMessage, StartChat
// and every call_llm build the person's available drivers, and each dynamic
// provider account answers from its catalog. A plain TTL cache made the first
// resolution after every expiry pay the provider's round trip: in prod
// (2026-11-26) about half of all SendMessage calls made a synchronous
// GET chatgpt.com/backend-api/codex/models (p50 443ms, p95 1.1s).
//
// The cache is stale-while-revalidate:
//
//   - A report younger than TTL is served as is.
//   - A report older than TTL but younger than MaxStale is served at once, and
//     one background fetch (per key, however many callers ask) replaces it.
//   - A failed fetch that is an outage (a timeout, a 5xx) never replaces a good
//     report: the last good one keeps serving until MaxStale, and the fetch is
//     retried after FailureTTL.
//   - A failed fetch that is a verdict (Policy.Verdict, e.g. the provider
//     refusing the credential) is served as the answer for FailureTTL and then
//     asked again synchronously, so a credential the provider accepts again is
//     usable on the very next resolution.
//   - With nothing servable (a cold key, a report past MaxStale, an expired
//     verdict) the caller waits for one fetch, shared with every concurrent
//     caller of that key.
//
// Fetches run on a context detached from the caller's cancellation (bounded by
// Policy.Timeout), so a caller that gives up never fails the fetch other
// callers share.
//
// Leaf utility package: a generic in-memory cache with no collaborator to fake.
// Its callers' fetches are the seam their tests replace.
//
//forge:exclude-contract: generic in-memory stale-while-revalidate cache; callers inject the fetch
package catalogcache

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Policy is a cache's timing and error classification.
type Policy struct {
	// TTL is how long a fetched report is fresh: served with no fetch.
	TTL time.Duration
	// MaxStale is how long after its fetch a report may still be served while
	// a background fetch replaces it. Past it the caller waits for a fetch.
	MaxStale time.Duration
	// FailureTTL is how long a failed fetch is remembered before it is retried.
	FailureTTL time.Duration
	// Timeout bounds each fetch.
	Timeout time.Duration
	// Verdict reports whether a failed fetch is the provider's answer (served
	// in place of any earlier report) rather than an outage (hidden behind the
	// last good report). Nil treats every failure as an outage.
	Verdict func(error) bool
}

// Cache is a stale-while-revalidate cache of V per key. The zero value is not
// usable; build one with New.
type Cache[V any] struct {
	policy Policy
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*entry[V]
}

type entry[V any] struct {
	good   V
	goodAt time.Time // zero: no successful fetch yet

	err   error // non-nil: the latest fetch failed
	errAt time.Time

	inflight *call[V]
}

type call[V any] struct {
	done chan struct{}
	val  V
	err  error
}

// New returns an empty cache with policy p.
func New[V any](p Policy) *Cache[V] {
	return &Cache[V]{policy: p, now: time.Now, entries: map[string]*entry[V]{}}
}

// Get returns key's report, calling fetch when nothing servable is cached (see
// the package doc). fetch receives a context that carries ctx's values but not
// its cancellation, bounded by Policy.Timeout.
func (c *Cache[V]) Get(ctx context.Context, key string, fetch func(context.Context) (V, error)) (V, error) {
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		e = &entry[V]{}
		c.entries[key] = e
	}
	now := c.now()
	if v, ok, refresh, err := c.servable(e, now); ok {
		if refresh {
			c.startLocked(ctx, key, e, fetch)
		}
		c.mu.Unlock()
		return v, err
	}
	cl := c.startLocked(ctx, key, e, fetch)
	c.mu.Unlock()

	select {
	case <-cl.done:
		return cl.val, cl.err
	case <-ctx.Done():
		var zero V
		return zero, fmt.Errorf("waiting for the catalog fetch: %w", ctx.Err())
	}
}

// servable decides what e answers at now without a fetch: ok reports that it
// answers at all, and refresh that a background fetch should replace it.
func (c *Cache[V]) servable(e *entry[V], now time.Time) (v V, ok, refresh bool, err error) {
	p := c.policy
	hasGood := !e.goodAt.IsZero() && now.Sub(e.goodAt) < p.MaxStale
	if e.err == nil {
		if e.goodAt.IsZero() {
			return v, false, false, nil
		}
		age := now.Sub(e.goodAt)
		if age < p.TTL {
			return e.good, true, false, nil
		}
		if hasGood {
			return e.good, true, true, nil
		}
		return v, false, false, nil
	}
	failedRecently := now.Sub(e.errAt) < p.FailureTTL
	if p.Verdict != nil && p.Verdict(e.err) {
		if failedRecently {
			return v, true, false, e.err
		}
		return v, false, false, nil
	}
	// An outage: the last good report, when there is one, hides it.
	if hasGood {
		return e.good, true, !failedRecently, nil
	}
	if failedRecently {
		return v, true, false, e.err
	}
	return v, false, false, nil
}

// startLocked returns key's in-flight fetch, starting one when there is none.
// c.mu must be held.
func (c *Cache[V]) startLocked(ctx context.Context, key string, e *entry[V], fetch func(context.Context) (V, error)) *call[V] {
	if e.inflight != nil {
		return e.inflight
	}
	cl := &call[V]{done: make(chan struct{})}
	e.inflight = cl
	fetchCtx := context.WithoutCancel(ctx)
	go func() {
		fctx, cancel := context.WithTimeout(fetchCtx, c.policy.Timeout)
		defer cancel()
		cl.val, cl.err = fetch(fctx)

		c.mu.Lock()
		now := c.now()
		if cl.err == nil {
			e.good, e.goodAt, e.err, e.errAt = cl.val, now, nil, time.Time{}
		} else {
			e.err, e.errAt = cl.err, now
		}
		e.inflight = nil
		c.pruneLocked(now)
		c.mu.Unlock()
		close(cl.done)
	}()
	return cl
}

// pruneLocked drops entries nothing can serve any more, so the map holds only
// the credentials seen recently. c.mu must be held.
func (c *Cache[V]) pruneLocked(now time.Time) {
	for key, e := range c.entries {
		if e.inflight != nil {
			continue
		}
		goodGone := e.goodAt.IsZero() || now.Sub(e.goodAt) >= c.policy.MaxStale
		errGone := e.err == nil || now.Sub(e.errAt) >= c.policy.FailureTTL
		if goodGone && errGone {
			delete(c.entries, key)
		}
	}
}

// Store records a fetch's outcome for key as if it had just landed: v when err
// is nil, else the failure. Tests seed the cache with it.
func (c *Cache[V]) Store(key string, v V, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		e = &entry[V]{}
		c.entries[key] = e
	}
	now := c.now()
	if err == nil {
		e.good, e.goodAt, e.err, e.errAt = v, now, nil, time.Time{}
		return
	}
	e.err, e.errAt = err, now
}

// Peek returns key's last good report without fetching, while it is servable
// (younger than MaxStale) and the latest fetch was not a verdict.
func (c *Cache[V]) Peek(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	e := c.entries[key]
	if e == nil || e.goodAt.IsZero() || c.now().Sub(e.goodAt) >= c.policy.MaxStale {
		return zero, false
	}
	if e.err != nil && c.policy.Verdict != nil && c.policy.Verdict(e.err) {
		return zero, false
	}
	return e.good, true
}

// Evict forgets key, so its next Get waits for a fresh fetch. For an answer
// known to be wrong: the provider refused a model its cached report offers, or
// the account was just reconnected.
func (c *Cache[V]) Evict(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Detach even an in-flight entry. Its existing waiters can still receive
	// their result, but subsequent callers must start a new fetch; the old
	// fetch must never restore the evicted catalog.
	delete(c.entries, key)
}

// Expire makes key's cached answer due: a good report is served stale while
// it is replaced, a remembered failure is retried. Tests use it to stand in
// for the TTL elapsing.
func (c *Cache[V]) Expire(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return
	}
	now := c.now()
	if !e.goodAt.IsZero() {
		e.goodAt = now.Add(-c.policy.TTL)
	}
	if e.err != nil {
		e.errAt = now.Add(-c.policy.FailureTTL)
	}
}

// Wait blocks until key has no fetch in flight, or ctx is done. Tests use it
// to observe a background refresh landing.
func (c *Cache[V]) Wait(ctx context.Context, key string) error {
	c.mu.Lock()
	e := c.entries[key]
	var cl *call[V]
	if e != nil {
		cl = e.inflight
	}
	c.mu.Unlock()
	if cl == nil {
		return nil
	}
	select {
	case <-cl.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
