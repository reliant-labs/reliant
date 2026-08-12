// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Default limits.
//
// These bound damage rather than police usage, so they are set well above what
// a person driving an assistant produces and well below what a runaway loop
// does. The failure mode being defended against is specific: a model that
// misreads a refusal as transient and retries forever, or one talked into a
// loop by injected text. Both look like hundreds of calls a minute from a
// caller that a human would notice in ten.
const (
	// defaultRequestsPerMinute is the sustained ceiling per connector.
	defaultRequestsPerMinute = 120

	// defaultBurst allows a short flurry — a model reading several files
	// before it starts working — without tripping the sustained limit.
	defaultBurst = 30

	// defaultMaxConcurrent bounds simultaneous in-flight calls. A daemon runs
	// one workspace; dozens of parallel commands do not make it faster, they
	// make it thrash and evict on memory.
	defaultMaxConcurrent = 4

	// idleLimiterTTL is how long an unused limiter is kept before cleanup.
	// Long enough to span a normal pause in a session, short enough that
	// churned connectors do not accumulate.
	idleLimiterTTL = 30 * time.Minute
)

// ErrRateLimited means the connector exceeded its request rate.
var ErrRateLimited = errors.New("too many requests")

// ErrTooManyConcurrent means the connector has too many calls in flight.
var ErrTooManyConcurrent = errors.New("too many concurrent requests")

// Limits configures a Limiter.
type Limits struct {
	RequestsPerMinute int
	Burst             int
	MaxConcurrent     int
}

func (l Limits) withDefaults() Limits {
	if l.RequestsPerMinute <= 0 {
		l.RequestsPerMinute = defaultRequestsPerMinute
	}
	if l.Burst <= 0 {
		l.Burst = defaultBurst
	}
	if l.MaxConcurrent <= 0 {
		l.MaxConcurrent = defaultMaxConcurrent
	}
	return l
}

// Limiter enforces per-connector request rate and concurrency.
//
// Limits are per GRANT, not per user or per credential: a grant is the unit a
// user creates, sees, and revokes, so it is the unit whose runaway behavior
// they can actually act on. One noisy connector must not exhaust a workspace
// that the user's other connectors — or the user's own session — are sharing.
type Limiter struct {
	limits Limits

	mu     sync.Mutex
	perKey map[string]*grantLimiter
}

type grantLimiter struct {
	rate     *rate.Limiter
	inFlight int
	lastSeen time.Time
}

// NewLimiter builds a Limiter. A zero Limits uses the defaults.
func NewLimiter(limits Limits) *Limiter {
	return &Limiter{
		limits: limits.withDefaults(),
		perKey: make(map[string]*grantLimiter),
	}
}

// Acquire reserves capacity for one call. The returned release function must
// be called when the call finishes; it is a no-op on error.
func (l *Limiter) Acquire(grantID string) (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.perKey[grantID]
	if !ok {
		entry = &grantLimiter{
			rate: rate.NewLimiter(
				rate.Limit(float64(l.limits.RequestsPerMinute)/60.0),
				l.limits.Burst,
			),
		}
		l.perKey[grantID] = entry
	}
	entry.lastSeen = time.Now()

	// Concurrency first. A caller that is already saturating the workspace
	// should be told that specifically, rather than being told to slow down
	// when its rate is fine.
	if entry.inFlight >= l.limits.MaxConcurrent {
		return func() {}, fmt.Errorf("%w: at most %d commands may run at once for this connector",
			ErrTooManyConcurrent, l.limits.MaxConcurrent)
	}

	if !entry.rate.Allow() {
		return func() {}, fmt.Errorf("%w: this connector is limited to %d requests per minute",
			ErrRateLimited, l.limits.RequestsPerMinute)
	}

	entry.inFlight++

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if e, ok := l.perKey[grantID]; ok && e.inFlight > 0 {
				e.inFlight--
			}
		})
	}, nil
}

// Cleanup drops limiters idle beyond the TTL. Callers run it periodically;
// without it a service that churns connectors accumulates one entry per grant
// for the process lifetime.
func (l *Limiter) Cleanup() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := time.Now().Add(-idleLimiterTTL)
	for key, entry := range l.perKey {
		// Never evict an entry with work in flight: its release would then
		// decrement a fresh entry and corrupt the count.
		if entry.inFlight == 0 && entry.lastSeen.Before(cutoff) {
			delete(l.perKey, key)
		}
	}
}

// RunCleanup runs Cleanup periodically until ctx ends.
func (l *Limiter) RunCleanup(ctx context.Context) {
	if l == nil {
		return
	}
	ticker := time.NewTicker(idleLimiterTTL / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.Cleanup()
		case <-ctx.Done():
			return
		}
	}
}
