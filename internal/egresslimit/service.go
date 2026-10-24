// Copyright (c) 2025 Reliant Labs

package egresslimit

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/reliant-labs/reliant/internal/observability"
)

// Limits are the egress budgets. Zero fields take the defaults.
type Limits struct {
	// Replicas is how many replicas share the rate budgets (min 1). Each
	// replica enforces 1/Replicas of every *PerMinute and *Burst value.
	Replicas int

	// UserPerMinute and UserBurst are the fleet-wide per-user token bucket.
	UserPerMinute int
	UserBurst     int
	// OrgPerMinute and OrgBurst are the fleet-wide per-org token bucket.
	OrgPerMinute int
	OrgBurst     int

	// UserMaxConcurrent and OrgMaxConcurrent are per-replica in-flight caps.
	UserMaxConcurrent int
	OrgMaxConcurrent  int
	// HostMaxConcurrent is the per-replica in-flight cap on one destination
	// host across all users: it keeps one third party (api.github.com) from
	// exhausting the node's NAT ports for every tenant.
	HostMaxConcurrent int
	// TurnMaxParallel is how many egress calls of one conversation turn run
	// at once. A call over it WAITS for a slot rather than being refused.
	TurnMaxParallel int
}

// Defaults.
const (
	DefaultUserPerMinute     = 60
	DefaultUserBurst         = 20
	DefaultOrgPerMinute      = 300
	DefaultOrgBurst          = 60
	DefaultUserMaxConcurrent = 6
	DefaultOrgMaxConcurrent  = 24
	DefaultHostMaxConcurrent = 16
	DefaultTurnMaxParallel   = 4

	// concurrencyRetryAfter is the hint for an in-flight cap: those clear as
	// calls finish, in seconds, not on a refill schedule.
	concurrencyRetryAfter = 2 * time.Second

	minIdleTTL = 10 * time.Minute
)

// WithDefaults fills every zero field with its default.
func (l Limits) WithDefaults() Limits {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&l.Replicas, 1)
	def(&l.UserPerMinute, DefaultUserPerMinute)
	def(&l.UserBurst, DefaultUserBurst)
	def(&l.OrgPerMinute, DefaultOrgPerMinute)
	def(&l.OrgBurst, DefaultOrgBurst)
	def(&l.UserMaxConcurrent, DefaultUserMaxConcurrent)
	def(&l.OrgMaxConcurrent, DefaultOrgMaxConcurrent)
	def(&l.HostMaxConcurrent, DefaultHostMaxConcurrent)
	def(&l.TurnMaxParallel, DefaultTurnMaxParallel)
	return l
}

// Deps holds dependencies for the egresslimit package.
type Deps struct {
	Logger *slog.Logger
	// Clock defaults to the wall clock.
	Clock Clock
	// Limits are the budgets (zero fields default).
	Limits Limits
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// bucket is one subject's (user's or org's) rate limiter and in-flight count.
type bucket struct {
	limiter  *rate.Limiter
	inFlight int
	lastSeen time.Time
}

type turn struct {
	slots chan struct{}
	refs  int
}

type service struct {
	deps  Deps
	limit Limits

	// userRate and orgRate are the per-replica shares of the fleet budgets.
	userRate, orgRate   rate.Limit
	userBurst, orgBurst int
	idleTTL             time.Duration

	mu        sync.Mutex
	users     map[string]*bucket
	orgs      map[string]*bucket
	hosts     map[string]int
	turns     map[string]*turn
	lastSweep time.Time
}

// New creates the egress limiter.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Clock == nil {
		deps.Clock = wallClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	l := deps.Limits.WithDefaults()
	replicas := float64(l.Replicas)
	s := &service{
		deps:      deps,
		limit:     l,
		userRate:  rate.Limit(float64(l.UserPerMinute) / 60.0 / replicas),
		orgRate:   rate.Limit(float64(l.OrgPerMinute) / 60.0 / replicas),
		userBurst: share(l.UserBurst, l.Replicas),
		orgBurst:  share(l.OrgBurst, l.Replicas),
		users:     map[string]*bucket{},
		orgs:      map[string]*bucket{},
		hosts:     map[string]int{},
		turns:     map[string]*turn{},
	}
	// An idle bucket is dropped only once it has refilled completely, so
	// forgetting it loses nothing.
	refill := func(burst int, r rate.Limit) time.Duration {
		return time.Duration(float64(burst) / float64(r) * float64(time.Second))
	}
	s.idleTTL = max(minIdleTTL, 2*refill(s.userBurst, s.userRate), 2*refill(s.orgBurst, s.orgRate))
	return s, nil
}

// share is a replica's slice of a fleet-wide count, at least 1.
func share(total, replicas int) int {
	return max(1, int(math.Ceil(float64(total)/float64(replicas))))
}

// Acquire admits one egress call.
//
// Order matters. Rate tokens are reserved first (a refusal there is free and
// final), then the call waits for its turn slot holding nothing that counts
// against another caller, and only then claims the in-flight slots, so a call
// queued behind its own turn never inflates the user, org or host counts.
func (s *service) Acquire(ctx context.Context, req Request) (func(), error) {
	now := s.deps.Clock.Now()

	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}

	// 1. Rate: user, then org.
	s.mu.Lock()
	s.sweepLocked(now)
	user := s.bucketLocked(s.users, req.UserID, s.userRate, s.userBurst, now)
	var org *bucket
	if req.OrgID != "" {
		org = s.bucketLocked(s.orgs, req.OrgID, s.orgRate, s.orgBurst, now)
	}
	for _, step := range []struct {
		b     *bucket
		scope string
	}{{user, ScopeUserRate}, {org, ScopeOrgRate}} {
		if step.b == nil {
			continue
		}
		r := step.b.limiter.ReserveN(now, 1)
		if !r.OK() || r.DelayFrom(now) > 0 {
			retry := r.DelayFrom(now)
			if !r.OK() {
				retry = time.Minute
			}
			r.CancelAt(now)
			s.mu.Unlock()
			rollback()
			return nil, s.limited(req, &LimitedError{Scope: step.scope, RetryAfter: retry})
		}
		undo = append(undo, func() { r.CancelAt(now) })
	}
	s.mu.Unlock()

	// 2. Turn slot: the only step that waits.
	releaseTurn, err := s.acquireTurn(ctx, req.TurnKey)
	if err != nil {
		s.mu.Lock()
		rollback()
		s.mu.Unlock()
		return nil, err
	}

	// 3. In-flight slots: user, org, host. Refused, never queued.
	s.mu.Lock()
	var lerr *LimitedError
	switch {
	case user.inFlight >= s.limit.UserMaxConcurrent:
		lerr = &LimitedError{Scope: ScopeUserConcurrency, RetryAfter: concurrencyRetryAfter}
	case org != nil && org.inFlight >= s.limit.OrgMaxConcurrent:
		lerr = &LimitedError{Scope: ScopeOrgConcurrency, RetryAfter: concurrencyRetryAfter}
	case req.Host != "" && s.hosts[req.Host] >= s.limit.HostMaxConcurrent:
		lerr = &LimitedError{Scope: ScopeHostConcurrency, RetryAfter: concurrencyRetryAfter, Host: req.Host}
	}
	if lerr != nil {
		rollback()
		s.mu.Unlock()
		releaseTurn()
		return nil, s.limited(req, lerr)
	}
	user.inFlight++
	user.lastSeen = now
	if org != nil {
		org.inFlight++
		org.lastSeen = now
	}
	if req.Host != "" {
		s.hosts[req.Host]++
	}
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			user.inFlight--
			if org != nil {
				org.inFlight--
			}
			if req.Host != "" {
				if s.hosts[req.Host]--; s.hosts[req.Host] <= 0 {
					delete(s.hosts, req.Host)
				}
			}
			s.mu.Unlock()
			releaseTurn()
		})
	}, nil
}

func (s *service) limited(req Request, e *LimitedError) error {
	observability.EgressLimitedTotal.WithLabelValues(req.Tool, e.Scope).Inc()
	s.deps.Logger.Warn("egress call rate limited",
		"tool", req.Tool, "scope", e.Scope, "user_id", req.UserID, "org_id", req.OrgID,
		"host", req.Host, "retry_after", e.RetryAfter)
	return e
}

// bucketLocked returns the subject's bucket, creating it on first use.
func (s *service) bucketLocked(m map[string]*bucket, key string, r rate.Limit, burst int, now time.Time) *bucket {
	b, ok := m[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(r, burst)}
		m[key] = b
	}
	b.lastSeen = now
	return b
}

// sweepLocked drops buckets idle longer than idleTTL. Run at most once a
// minute, on the Acquire path, so the limiter needs no goroutine to stop.
func (s *service) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < time.Minute {
		return
	}
	s.lastSweep = now
	for _, m := range []map[string]*bucket{s.users, s.orgs} {
		for k, b := range m {
			if b.inFlight == 0 && now.Sub(b.lastSeen) > s.idleTTL {
				delete(m, k)
			}
		}
	}
}

func (s *service) acquireTurn(ctx context.Context, key string) (func(), error) {
	if key == "" {
		return func() {}, nil
	}
	s.mu.Lock()
	t, ok := s.turns[key]
	if !ok {
		t = &turn{slots: make(chan struct{}, s.limit.TurnMaxParallel)}
		s.turns[key] = t
	}
	t.refs++
	s.mu.Unlock()

	drop := func() {
		s.mu.Lock()
		if t.refs--; t.refs == 0 {
			delete(s.turns, key)
		}
		s.mu.Unlock()
	}
	select {
	case t.slots <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	return func() {
		<-t.slots
		drop()
	}, nil
}
