// Copyright (c) 2025 Reliant Labs

package egresslimit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTest(t *testing.T, l Limits) (Service, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	svc, err := New(Deps{Clock: clk, Limits: l})
	require.NoError(t, err)
	return svc, clk
}

// roomy keeps every limit out of the way except the ones a test sets.
func roomy(l Limits) Limits {
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&l.UserPerMinute, 1_000_000)
	def(&l.UserBurst, 1_000_000)
	def(&l.OrgPerMinute, 1_000_000)
	def(&l.OrgBurst, 1_000_000)
	def(&l.UserMaxConcurrent, 1_000_000)
	def(&l.OrgMaxConcurrent, 1_000_000)
	def(&l.HostMaxConcurrent, 1_000_000)
	def(&l.TurnMaxParallel, 1_000_000)
	return l
}

func mustLimited(t *testing.T, err error, scope string) *LimitedError {
	t.Helper()
	var le *LimitedError
	require.ErrorAs(t, err, &le)
	require.Equal(t, scope, le.Scope)
	require.GreaterOrEqual(t, le.RetryAfterSeconds(), 1)
	require.Contains(t, le.Error(), "rate limited")
	require.Contains(t, le.Error(), "retry after")
	return le
}

func TestBurstBeyondLimitIsRefusedWithRetryAfter(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{UserPerMinute: 60, UserBurst: 3}))
	req := Request{Tool: "fetch", UserID: "u1"}
	for i := 0; i < 3; i++ {
		release, err := svc.Acquire(context.Background(), req)
		require.NoError(t, err, "call %d is inside the burst", i)
		release()
	}
	_, err := svc.Acquire(context.Background(), req)
	le := mustLimited(t, err, ScopeUserRate)
	require.Equal(t, 1, le.RetryAfterSeconds(), "60/min refills one token a second")
}

func TestRateRefillsOverTime(t *testing.T) {
	svc, clk := newTest(t, roomy(Limits{UserPerMinute: 60, UserBurst: 1}))
	req := Request{Tool: "fetch", UserID: "u1"}
	release, err := svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	release()
	_, err = svc.Acquire(context.Background(), req)
	mustLimited(t, err, ScopeUserRate)

	clk.Advance(1100 * time.Millisecond)
	release, err = svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	release()
}

func TestRefusedCallDoesNotSpendTokens(t *testing.T) {
	svc, clk := newTest(t, roomy(Limits{UserPerMinute: 60, UserBurst: 1}))
	req := Request{Tool: "fetch", UserID: "u1"}
	release, err := svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	release()
	for i := 0; i < 50; i++ {
		_, err = svc.Acquire(context.Background(), req)
		mustLimited(t, err, ScopeUserRate)
	}
	// Fifty refusals must not push the next admission into the future.
	clk.Advance(1100 * time.Millisecond)
	release, err = svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	release()
}

func TestTwoUsersAreIndependent(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{UserPerMinute: 60, UserBurst: 1, UserMaxConcurrent: 1}))
	relA, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "alice"})
	require.NoError(t, err)

	// Alice is out of rate AND concurrency; Bob is untouched by it.
	_, err = svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "alice"})
	mustLimited(t, err, ScopeUserRate)
	relB, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "bob"})
	require.NoError(t, err)
	relA()
	relB()
}

func TestOrgLimitSpansUsers(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{OrgPerMinute: 60, OrgBurst: 2}))
	for _, u := range []string{"u1", "u2"} {
		release, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: u, OrgID: "acme"})
		require.NoError(t, err)
		release()
	}
	_, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u3", OrgID: "acme"})
	mustLimited(t, err, ScopeOrgRate)

	release, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u4", OrgID: "other"})
	require.NoError(t, err, "another org is unaffected")
	release()
	release, err = svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u5"})
	require.NoError(t, err, "a caller with no org is not charged to one")
	release()
}

func TestOrgRefusalRefundsTheUsersToken(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{UserPerMinute: 60, UserBurst: 2, OrgPerMinute: 60, OrgBurst: 1}))
	release, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", OrgID: "acme"})
	require.NoError(t, err)
	release()
	_, err = svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", OrgID: "acme"})
	mustLimited(t, err, ScopeOrgRate)
	// The refused call took nothing from u1: with no org it still has its second token.
	release, err = svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1"})
	require.NoError(t, err)
	release()
}

func TestUserConcurrencyCap(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{UserMaxConcurrent: 2}))
	req := Request{Tool: "fetch", UserID: "u1"}
	r1, err := svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	r2, err := svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	_, err = svc.Acquire(context.Background(), req)
	mustLimited(t, err, ScopeUserConcurrency)

	r1()
	r1() // releasing twice frees one slot, not two
	r3, err := svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	_, err = svc.Acquire(context.Background(), req)
	mustLimited(t, err, ScopeUserConcurrency)
	r2()
	r3()
}

func TestOrgConcurrencyCap(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{OrgMaxConcurrent: 2}))
	r1, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", OrgID: "acme"})
	require.NoError(t, err)
	r2, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u2", OrgID: "acme"})
	require.NoError(t, err)
	_, err = svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u3", OrgID: "acme"})
	mustLimited(t, err, ScopeOrgConcurrency)
	r1()
	r2()
}

func TestPerHostCapHoldsAcrossUsers(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{HostMaxConcurrent: 2}))
	gh := func(u string) Request { return Request{Tool: "fetch", UserID: u, Host: "api.github.com"} }
	r1, err := svc.Acquire(context.Background(), gh("alice"))
	require.NoError(t, err)
	r2, err := svc.Acquire(context.Background(), gh("bob"))
	require.NoError(t, err)

	_, err = svc.Acquire(context.Background(), gh("carol"))
	le := mustLimited(t, err, ScopeHostConcurrency)
	require.Equal(t, "api.github.com", le.Host)
	require.Contains(t, le.Error(), "api.github.com")

	other, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "carol", Host: "example.com"})
	require.NoError(t, err, "another host is unaffected")
	other()

	r1()
	r3, err := svc.Acquire(context.Background(), gh("carol"))
	require.NoError(t, err, "a finished call frees the host slot")
	r2()
	r3()
}

func TestRefusalDoesNotLeakSlots(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{HostMaxConcurrent: 1, UserMaxConcurrent: 5}))
	held, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", Host: "h"})
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		_, err = svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", Host: "h"})
		mustLimited(t, err, ScopeHostConcurrency)
	}
	// 20 refusals must not have consumed u1's user slots.
	for i := 0; i < 4; i++ {
		r, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", Host: "free" + string(rune('a'+i))})
		require.NoError(t, err)
		defer r()
	}
	held()
}

func TestTurnParallelismIsBoundedByWaiting(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{TurnMaxParallel: 2}))
	const calls = 8
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", TurnKey: "chat/thread"})
			if err != nil {
				t.Errorf("a call over the turn bound waits, it is not refused: %v", err)
				return
			}
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			release()
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, int(peak.Load()), 2)
}

func TestTurnsAreIndependent(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{TurnMaxParallel: 1}))
	r1, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", TurnKey: "a"})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r2, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", TurnKey: "b"})
		if err == nil {
			r2()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a different turn must not wait on this one")
	}
	r1()
}

func TestWaitingForTurnSlotIsCancellable(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{TurnMaxParallel: 1, UserMaxConcurrent: 1}))
	held, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", TurnKey: "t"})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = svc.Acquire(ctx, Request{Tool: "fetch", UserID: "u1", TurnKey: "t"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var le *LimitedError
	require.False(t, errors.As(err, &le), "a cancelled wait is not a rate limit")

	held()
	r, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1", TurnKey: "t"})
	require.NoError(t, err, "the cancelled waiter left nothing held")
	r()
}

// Multi-replica behaviour: the fleet budget is split across Replicas, so N
// replicas together admit the configured budget, not N times it.
func TestReplicasShareTheFleetRateBudget(t *testing.T) {
	const replicas = 4
	fleetBurst := 20
	var admitted int
	for r := 0; r < replicas; r++ {
		svc, _ := newTest(t, roomy(Limits{Replicas: replicas, UserPerMinute: 60, UserBurst: fleetBurst}))
		// A hostile client that spreads evenly over every replica and keeps going.
		for i := 0; i < 100; i++ {
			release, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u1"})
			if err == nil {
				admitted++
				release()
			}
		}
	}
	require.Equal(t, fleetBurst, admitted, "the fleet admits the burst budget once, not once per replica")
}

func TestReplicasDoNotDivideConcurrencyCaps(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{Replicas: 4, HostMaxConcurrent: 3}))
	var rels []func()
	for i := 0; i < 3; i++ {
		r, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u", Host: "h"})
		require.NoError(t, err, "in-flight caps bound one process and one node's NAT, so they are per replica")
		rels = append(rels, r)
	}
	_, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u", Host: "h"})
	mustLimited(t, err, ScopeHostConcurrency)
	for _, r := range rels {
		r()
	}
}

func TestLimitedCallsEmitMetric(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{UserPerMinute: 60, UserBurst: 1}))
	counter := observability.EgressLimitedTotal.WithLabelValues("metric_probe", ScopeUserRate)
	before := testutil.ToFloat64(counter)
	req := Request{Tool: "metric_probe", UserID: "u1"}
	release, err := svc.Acquire(context.Background(), req)
	require.NoError(t, err)
	release()
	_, err = svc.Acquire(context.Background(), req)
	require.Error(t, err)
	require.Equal(t, before+1, testutil.ToFloat64(counter))
}

func TestIdleBucketsAreSwept(t *testing.T) {
	svc, clk := newTest(t, roomy(Limits{}))
	s := svc.(*service)
	for _, u := range []string{"a", "b", "c"} {
		r, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: u, OrgID: "o-" + u})
		require.NoError(t, err)
		r()
	}
	require.Len(t, s.users, 3)
	clk.Advance(s.idleTTL + 2*time.Minute)
	r, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "d"})
	require.NoError(t, err)
	r()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.users, 1)
	require.Empty(t, s.orgs)
	require.Empty(t, s.hosts)
	require.Empty(t, s.turns)
}

// Hammered from many goroutines, counts never go negative or stick above the cap.
func TestConcurrentAcquireReleaseKeepsCountsExact(t *testing.T) {
	svc, _ := newTest(t, roomy(Limits{UserMaxConcurrent: 3, HostMaxConcurrent: 3, TurnMaxParallel: 2}))
	s := svc.(*service)
	var running atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				release, err := svc.Acquire(context.Background(), Request{Tool: "fetch", UserID: "u", OrgID: "o", Host: "h", TurnKey: "t"})
				if err != nil {
					continue
				}
				if n := running.Add(1); n > 2 {
					t.Errorf("turn bound exceeded: %d running", n)
				}
				running.Add(-1)
				release()
			}
		}()
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Zero(t, s.users["u"].inFlight)
	require.Zero(t, s.orgs["o"].inFlight)
	require.Empty(t, s.hosts)
	require.Empty(t, s.turns)
}
