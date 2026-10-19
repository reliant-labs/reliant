// Copyright (c) 2025 Reliant Labs

package accountrouter

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

var t0 = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// fakeStore is an in-memory Store with the compare-and-swap semantics the
// real one has.
type fakeStore struct {
	mu          sync.Mutex
	assignments map[string]Assignment // threadID|provider
	history     []Assignment
	limits      map[string]LimitState
	getErr      error
	setErr      error
	limitsErr   error
	recentErr   error
	// beforeSet runs inside SetThreadAssignment before the CAS, to stage a
	// concurrent writer.
	beforeSet func(s *fakeStore, a Assignment)
	sets      int
}

func newStore() *fakeStore {
	return &fakeStore{assignments: map[string]Assignment{}, limits: map[string]LimitState{}}
}

func (s *fakeStore) key(thread, provider string) string { return thread + "|" + provider }

func (s *fakeStore) GetThreadAssignment(_ context.Context, threadID, provider string) (*Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	a, ok := s.assignments[s.key(threadID, provider)]
	if !ok {
		return nil, nil
	}
	return &a, nil
}

func (s *fakeStore) SetThreadAssignment(_ context.Context, a Assignment, expected string) (bool, error) {
	s.mu.Lock()
	hook := s.beforeSet
	s.mu.Unlock()
	if hook != nil {
		hook(s, a)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return false, s.setErr
	}
	cur, ok := s.assignments[s.key(a.ThreadID, a.Provider)]
	if expected != "" && (!ok || cur.AccountID != expected) {
		return false, nil
	}
	if expected == "" && ok {
		return false, nil
	}
	s.assignments[s.key(a.ThreadID, a.Provider)] = a
	s.history = append(s.history, a)
	return true, nil
}

func (s *fakeStore) ListAccountLimits(context.Context, string) (map[string]LimitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limitsErr != nil {
		return nil, s.limitsErr
	}
	out := make(map[string]LimitState, len(s.limits))
	for k, v := range s.limits {
		out[k] = v
	}
	return out, nil
}

func (s *fakeStore) RecentAssignmentCounts(_ context.Context, ids []string, since time.Time) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recentErr != nil {
		return nil, s.recentErr
	}
	out := map[string]int{}
	for _, id := range ids {
		for _, a := range s.history {
			if a.AccountID == id && !a.AssignedAt.Before(since) {
				out[id]++
			}
		}
	}
	return out, nil
}

func (s *fakeStore) assign(thread, provider, account string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignments[s.key(thread, provider)] = Assignment{ThreadID: thread, Provider: provider, AccountID: account, AssignedAt: t0}
}

func acct(id string, priority int) models.AccountConfig {
	return models.AccountConfig{
		DriverConfig: models.DriverConfig{
			DriverID: "claude", AccountID: id, AccountEmail: id + "@example.com", AccountLabel: "label-" + id,
		},
		Priority: priority,
		Status:   models.AccountAvailable,
	}
}

func frac(f float64) *float64 { return &f }

func window(name string, used float64, resetsIn time.Duration, length time.Duration) LimitWindow {
	return LimitWindow{Name: name, UsedFraction: frac(used), ResetsAt: t0.Add(resetsIn), LengthS: int64(length / time.Second)}
}

func usage(id string, ws ...LimitWindow) LimitState {
	return LimitState{AccountID: id, Status: "allowed", Windows: ws, ObservedAt: t0}
}

func benched(id string, until time.Time, win string) LimitState {
	return LimitState{AccountID: id, Status: "rejected", LimitedUntil: &until, LimitedWindow: win, ObservedAt: t0}
}

func (s *fakeStore) put(l LimitState) { s.limits[l.AccountID] = l }

// newRouter builds a router at t0 whose Rand picks index pick(n).
func newRouter(t *testing.T, s Store, pick func(n int) int) Service {
	t.Helper()
	return New(Deps{Store: s, Now: func() time.Time { return t0 }, Rand: pick})
}

func first(int) int { return 0 }

func req(thread string, accounts ...models.AccountConfig) Request {
	return Request{UserID: "u1", ThreadID: thread, Driver: "claude", ModelID: "claude-5.5-sonnet", Accounts: accounts, Mode: RoutingBalance}
}

func TestStickyAssignmentStaysWhileEligible(t *testing.T) {
	for _, mode := range []RoutingMode{RoutingBalance, RoutingPriority} {
		t.Run(string(mode), func(t *testing.T) {
			s := newStore()
			s.assign("th", "claude", "b")
			s.put(usage("a", window("5h", 0.0, time.Hour, 5*time.Hour)))
			s.put(usage("b", window("5h", 0.9, time.Hour, 5*time.Hour)))
			r := newRouter(t, s, first)

			rq := req("th", acct("a", 0), acct("b", 1))
			rq.Mode = mode
			res, err := r.Assign(context.Background(), rq)

			require.NoError(t, err)
			assert.Equal(t, "b", res.Account.AccountID, "a sticky thread is never moved for balance or priority")
			assert.False(t, res.Changed)
			assert.Nil(t, res.Previous)
			assert.Zero(t, s.sets, "an unchanged assignment writes nothing")
		})
	}
}

func TestStickyAssignmentIgnoresPreferredAccount(t *testing.T) {
	s := newStore()
	s.assign("th", "claude", "b")
	rq := req("th", acct("a", 0), acct("b", 1))
	rq.Preferred = "claude:a"

	res, err := newRouter(t, s, first).Assign(context.Background(), rq)

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID)
	assert.False(t, res.Changed)
}

func TestBalanceLowestBandWins(t *testing.T) {
	s := newStore()
	s.put(usage("a", window("5h", 0.60, time.Hour, 5*time.Hour)))
	s.put(usage("b", window("5h", 0.20, time.Hour, 5*time.Hour)))
	s.put(usage("c", window("5h", 0.45, time.Hour, 5*time.Hour)))

	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1), acct("c", 2)))

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID)
	assert.Equal(t, ReasonInitial, res.Reason)
	assert.True(t, res.Changed)
	assert.Equal(t, "b", s.assignments["th|claude"].AccountID, "the pick is stored")
}

func TestBalanceRandomOnlyWithinTheLowestBand(t *testing.T) {
	// a and b are within one 5-point band; c is a band above. Over many
	// seeds the pick must land on a or b, hit both, and never reach c.
	accounts := []models.AccountConfig{acct("a", 0), acct("b", 1), acct("c", 2)}

	seen := map[string]int{}
	for seed := range uint64(60) {
		s := newStore() // fresh: earlier picks must not add herd penalty
		s.put(usage("a", window("5h", 0.10, time.Hour, 5*time.Hour)))
		s.put(usage("b", window("5h", 0.14, time.Hour, 5*time.Hour)))
		s.put(usage("c", window("5h", 0.21, time.Hour, 5*time.Hour)))
		rng := rand.New(rand.NewPCG(seed, 7))
		var asked []int
		r := newRouter(t, s, func(n int) int { asked = append(asked, n); return rng.IntN(n) })
		res, err := r.Assign(context.Background(), req("th", accounts...))
		require.NoError(t, err)
		seen[res.Account.AccountID]++
		assert.Equal(t, []int{2}, asked, "random is drawn over the two accounts in the band only")
	}
	assert.Zero(t, seen["c"])
	assert.Positive(t, seen["a"])
	assert.Positive(t, seen["b"])
}

func TestBalanceNoRandomWhenOneAccountIsAloneInItsBand(t *testing.T) {
	s := newStore()
	s.put(usage("a", window("5h", 0.5, time.Hour, 5*time.Hour)))
	s.put(usage("b", window("5h", 0.0, time.Hour, 5*time.Hour)))
	r := newRouter(t, s, func(n int) int { t.Fatalf("rand asked for n=%d with a single candidate in the band", n); return 0 })

	res, err := r.Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID)
}

func TestBalanceNoUsageKnownIsAllEqual(t *testing.T) {
	r := newRouter(t, newStore(), func(n int) int { return n - 1 })

	res, err := r.Assign(context.Background(), req("th", acct("a", 0), acct("b", 1), acct("c", 2)))

	require.NoError(t, err)
	assert.Equal(t, "c", res.Account.AccountID, "unknown usage is zero: all accounts share the band and rand decides")
}

func TestScoreIsTheMaxOverApplicableWindows(t *testing.T) {
	l := usage("a",
		window("5h", 0.10, time.Hour, 5*time.Hour),
		window("7d", 0.60, 24*time.Hour, 7*24*time.Hour),
	)
	score, binding := Score(l, "claude-5.5-sonnet", t0)
	assert.InDelta(t, 0.60, score, 1e-9)
	assert.Equal(t, "7d", binding, "a 7d window at 60% beats a 5h window at 10%")
}

func TestBalancePrefersTheAccountWhoseClosestWindowIsLeastFull(t *testing.T) {
	s := newStore()
	s.put(usage("a", window("5h", 0.10, time.Hour, 5*time.Hour), window("7d", 0.60, 24*time.Hour, 7*24*time.Hour)))
	s.put(usage("b", window("5h", 0.40, time.Hour, 5*time.Hour), window("7d", 0.20, 24*time.Hour, 7*24*time.Hour)))

	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID, "a: max(10%,60%)=60%, b: max(40%,20%)=40%")
}

func TestScoreCountsAWindowPastItsResetAsEmpty(t *testing.T) {
	l := usage("a", window("5h", 0.95, -time.Minute, 5*time.Hour))
	score, binding := Score(l, "claude-5.5-sonnet", t0)
	assert.Zero(t, score, "the window reset: it is a new one, used 0")
	assert.Empty(t, binding)
}

func TestScoreTreatsStaleObservationsAsUnknown(t *testing.T) {
	// Observed 6h ago, the 5h window's own length, with a reset still in the
	// future (a stale reset time): nothing believable remains.
	l := usage("a", window("5h", 0.95, time.Hour, 5*time.Hour))
	l.ObservedAt = t0.Add(-6 * time.Hour)
	score, _ := Score(l, "claude-5.5-sonnet", t0)
	assert.Zero(t, score)

	// The same observation, fresher than the window length, counts.
	l.ObservedAt = t0.Add(-4 * time.Hour)
	score, _ = Score(l, "claude-5.5-sonnet", t0)
	assert.InDelta(t, 0.95, score, 1e-9)
}

func TestScoreWithoutAnObservationIsUnknown(t *testing.T) {
	l := LimitState{Windows: []LimitWindow{window("5h", 0.9, time.Hour, 5*time.Hour)}}
	score, _ := Score(l, "claude-5.5-sonnet", t0)
	assert.Zero(t, score)
}

func TestScoreWindowWithOnlyLimitAndRemaining(t *testing.T) {
	limit, remaining := int64(100), int64(25)
	l := usage("a", LimitWindow{Name: "requests", Limit: &limit, Remaining: &remaining, ResetsAt: t0.Add(time.Minute)})
	score, binding := Score(l, "gpt-5.6", t0)
	assert.InDelta(t, 0.75, score, 1e-9)
	assert.Equal(t, "requests", binding)
}

func TestScoreModelScopedWindowsApplyOnlyToTheirFamily(t *testing.T) {
	l := usage("a",
		window("5h", 0.10, time.Hour, 5*time.Hour),
		window("seven_day_opus", 0.90, 24*time.Hour, 7*24*time.Hour),
		window("7d_sonnet", 0.50, 24*time.Hour, 7*24*time.Hour),
	)
	cases := []struct {
		model   string
		want    float64
		binding string
	}{
		{"claude-5.5-opus", 0.90, "seven_day_opus"},
		{"claude-5.5-sonnet", 0.50, "7d_sonnet"},
		{"claude-haiku-4.5", 0.10, "5h"},
		{"gpt-5.6-sol", 0.10, "5h"},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			score, binding := Score(l, c.model, t0)
			assert.InDelta(t, c.want, score, 1e-9)
			assert.Equal(t, c.binding, binding)
		})
	}
}

func TestScoreWindowWhoseScopeIsUnknownAppliesToEveryModel(t *testing.T) {
	l := usage("a", window("codex_other.primary", 0.8, time.Hour, 5*time.Hour))
	score, _ := Score(l, "gpt-5.6-sol", t0)
	assert.InDelta(t, 0.8, score, 1e-9)
}

func TestBalanceModelScopedWindowShapesThePick(t *testing.T) {
	s := newStore()
	s.put(usage("a", window("5h", 0.10, time.Hour, 5*time.Hour), window("seven_day_opus", 0.95, 24*time.Hour, 7*24*time.Hour)))
	s.put(usage("b", window("5h", 0.30, time.Hour, 5*time.Hour)))
	accounts := []models.AccountConfig{acct("a", 0), acct("b", 1)}
	r := newRouter(t, s, first)

	opus := req("t-opus", accounts...)
	opus.ModelID = "claude-5.5-opus"
	res, err := r.Assign(context.Background(), opus)
	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID, "a's Opus weekly is nearly gone")

	sonnet := req("t-sonnet", accounts...)
	res, err = r.Assign(context.Background(), sonnet)
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID, "the Opus weekly does not count against a Sonnet thread")
}

func TestHerdPenaltySpreadsConcurrentPicks(t *testing.T) {
	s := newStore()
	rng := rand.New(rand.NewPCG(1, 2))
	r := newRouter(t, s, rng.IntN)
	accounts := []models.AccountConfig{acct("a", 0), acct("b", 1), acct("c", 2)}

	counts := map[string]int{}
	for i := range 20 {
		res, err := r.Assign(context.Background(), req(fmt.Sprintf("sub-%d", i), accounts...))
		require.NoError(t, err)
		counts[res.Account.AccountID]++
	}

	for id, n := range counts {
		assert.LessOrEqual(t, n, 10, "account %s took %d of 20 threads", id, n)
	}
	assert.Len(t, counts, 3, "every account takes some of the threads")
}

func TestHerdPenaltySpreadsEvenWhenRandAlwaysPicksTheFirst(t *testing.T) {
	// The worst case for a herd: rand never helps, so only the penalty can
	// keep all twenty off one account.
	r := newRouter(t, newStore(), first)
	accounts := []models.AccountConfig{acct("a", 0), acct("b", 1), acct("c", 2)}
	counts := map[string]int{}
	for i := range 20 {
		res, err := r.Assign(context.Background(), req(fmt.Sprintf("sub-%d", i), accounts...))
		require.NoError(t, err)
		counts[res.Account.AccountID]++
	}
	for id, n := range counts {
		assert.LessOrEqual(t, n, 10, "account %s took %d of 20 threads", id, n)
	}
	assert.Len(t, counts, 3)
}

func TestHerdPenaltyAgesOut(t *testing.T) {
	s := newStore()
	// Four assignments to a, all older than the herd window: no penalty.
	for i := range 4 {
		s.history = append(s.history, Assignment{ThreadID: fmt.Sprint(i), AccountID: "a", AssignedAt: t0.Add(-10 * time.Minute)})
	}
	var asked []int
	r := newRouter(t, s, func(n int) int { asked = append(asked, n); return 0 })

	_, err := r.Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))

	require.NoError(t, err)
	assert.Equal(t, []int{2}, asked, "old assignments do not move an account out of the band")
}

func TestHerdPenaltyConcurrentAssignIsRaceFree(t *testing.T) {
	s := newStore()
	var mu sync.Mutex
	rng := rand.New(rand.NewPCG(3, 4))
	r := newRouter(t, s, func(n int) int { mu.Lock(); defer mu.Unlock(); return rng.IntN(n) })
	accounts := []models.AccountConfig{acct("a", 0), acct("b", 1), acct("c", 2)}

	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			th := fmt.Sprintf("th-%d", i%10) // three goroutines race on each thread
			res, err := r.Assign(context.Background(), req(th, accounts...))
			assert.NoError(t, err)
			assert.NotEmpty(t, res.Account.AccountID)
		}()
	}
	wg.Wait()

	for th := range 10 {
		a := s.assignments[fmt.Sprintf("th-%d|claude", th)]
		assert.NotEmpty(t, a.AccountID, "every thread ends with exactly one stored account")
	}
}

func TestPriorityModeFillsInOrder(t *testing.T) {
	s := newStore()
	// Usage points the other way: balance would pick c.
	s.put(usage("a", window("5h", 0.80, time.Hour, 5*time.Hour)))
	s.put(usage("b", window("5h", 0.40, time.Hour, 5*time.Hour)))
	s.put(usage("c", window("5h", 0.00, time.Hour, 5*time.Hour)))
	r := newRouter(t, s, func(n int) int { t.Fatal("priority mode never draws randomly"); return 0 })

	accounts := []models.AccountConfig{acct("c", 2), acct("a", 0), acct("b", 1)}
	for i := range 5 {
		rq := req(fmt.Sprintf("th-%d", i), accounts...)
		rq.Mode = RoutingPriority
		res, err := r.Assign(context.Background(), rq)
		require.NoError(t, err)
		assert.Equal(t, "a", res.Account.AccountID, "the lowest priority number takes every new thread")
	}
}

func TestPriorityModeOverflowsToTheNextAccountOnALimit(t *testing.T) {
	s := newStore()
	s.put(benched("a", t0.Add(time.Hour), "5h"))
	r := newRouter(t, s, first)
	rq := req("th", acct("a", 0), acct("b", 1), acct("c", 2))
	rq.Mode = RoutingPriority

	res, err := r.Assign(context.Background(), rq)

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID)
}

func TestPriorityModeStickyThreadMovesOnlyOnALimit(t *testing.T) {
	s := newStore()
	s.assign("th", "claude", "b")
	r := newRouter(t, s, first)
	rq := req("th", acct("a", 0), acct("b", 1))
	rq.Mode = RoutingPriority

	res, err := r.Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID, "a is higher priority but the thread stays")

	s.put(benched("b", t0.Add(time.Hour), "5h"))
	res, err = r.Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID)
	assert.Equal(t, ReasonLimited, res.Reason)
	assert.True(t, res.Changed)
	require.NotNil(t, res.Previous)
	assert.Equal(t, "b", res.Previous.AccountID)
}

func TestUnknownRoutingModeBalances(t *testing.T) {
	assert.Equal(t, RoutingBalance, ParseRoutingMode(""))
	assert.Equal(t, RoutingBalance, ParseRoutingMode("round-robin"))
	assert.Equal(t, RoutingBalance, ParseRoutingMode("balance"))
	assert.Equal(t, RoutingPriority, ParseRoutingMode("priority"))

	s := newStore()
	s.put(usage("a", window("5h", 0.9, time.Hour, 5*time.Hour)))
	rq := req("th", acct("a", 0), acct("b", 1))
	rq.Mode = ""
	res, err := newRouter(t, s, first).Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID, "the zero mode is Balance")
}

func TestPreferredInstanceWinsByIDEmailAndLabel(t *testing.T) {
	s := newStore()
	s.put(usage("a", window("5h", 0.0, time.Hour, 5*time.Hour)))
	s.put(usage("b", window("5h", 0.9, time.Hour, 5*time.Hour)))
	accounts := []models.AccountConfig{acct("a", 0), acct("b", 1)}

	for _, preferred := range []string{"claude:b", "claude:B@Example.com", "claude:label-b", "b", "b@example.com", " claude:b "} {
		t.Run(preferred, func(t *testing.T) {
			rq := req(fmt.Sprintf("th-%s", preferred), accounts...)
			rq.Preferred = preferred
			res, err := newRouter(t, newStoreWith(s), first).Assign(context.Background(), rq)
			require.NoError(t, err)
			assert.Equal(t, "b", res.Account.AccountID, "preferred beats the least-used pick")
			assert.Equal(t, ReasonPinned, res.Reason)
		})
	}
}

func newStoreWith(from *fakeStore) *fakeStore {
	s := newStore()
	for k, v := range from.limits {
		s.limits[k] = v
	}
	return s
}

func TestPreferredInstanceIsIgnoredWhenNotUsable(t *testing.T) {
	cases := map[string]func(s *fakeStore, accounts []models.AccountConfig){
		"limited": func(s *fakeStore, _ []models.AccountConfig) { s.put(benched("b", t0.Add(time.Hour), "5h")) },
		"unknown": func(_ *fakeStore, a []models.AccountConfig) { a[1].AccountID = "zzz" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore()
			accounts := []models.AccountConfig{acct("a", 0), acct("b", 1)}
			mutate(s, accounts)
			rq := req("th", accounts...)
			rq.Preferred = "claude:b"
			res, err := newRouter(t, s, first).Assign(context.Background(), rq)
			require.NoError(t, err)
			assert.Equal(t, "a", res.Account.AccountID)
			assert.Equal(t, ReasonInitial, res.Reason)
		})
	}
}

func TestPreferredInstanceOfAnotherDriverIsIgnored(t *testing.T) {
	rq := req("th", acct("a", 0), acct("b", 1))
	rq.Preferred = "codex:b"
	res, err := newRouter(t, newStore(), first).Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID)
}

func TestAllLimitedReturnsErrAllLimitedWithTheEarliestReset(t *testing.T) {
	s := newStore()
	s.put(benched("a", t0.Add(3*time.Hour), "7d"))
	s.put(benched("b", t0.Add(90*time.Minute), "5h"))
	s.put(benched("c", t0.Add(2*time.Hour), "5h"))
	// Sticky thread on a limited account: still no account, assignment kept.
	s.assign("th", "claude", "a")

	_, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1), acct("c", 2)))

	var all *ErrAllLimited
	require.ErrorAs(t, err, &all)
	assert.Equal(t, models.DriverID("claude"), all.Driver)
	assert.Equal(t, t0.Add(90*time.Minute), all.EarliestReset)
	assert.Equal(t, "5h", all.Window)
	assert.Contains(t, all.Error(), "Claude subscription")
	assert.Equal(t, "a", s.assignments["th|claude"].AccountID, "nothing was written")
}

func TestLimitedUntilInThePastIsNotLimited(t *testing.T) {
	s := newStore()
	s.put(benched("a", t0.Add(-time.Second), "5h"))

	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0)))

	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID, "the bench is lazy: limited_until <= now is available")
}

func TestLimitedUntilOnTheConfigAlsoLimits(t *testing.T) {
	a := acct("a", 0)
	a.Status = models.AccountLimited
	a.LimitedUntil = t0.Add(time.Hour)

	res, err := newRouter(t, newStore(), first).Assign(context.Background(), req("th", a, acct("b", 1)))

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID)
}

func TestNoUsableAccountIsProviderUnavailableNotAllLimited(t *testing.T) {
	_, err := newRouter(t, newStore(), first).Assign(context.Background(), req("th"))
	var unavailable *models.ProviderUnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, []string{"claude"}, unavailable.Providers)
	var all *ErrAllLimited
	assert.False(t, errors.As(err, &all))
}

func TestAccountsThatDoNotServeTheModelAreSkipped(t *testing.T) {
	plus, pro := acct("plus", 0), acct("pro", 1)
	plus.Serves = func(model string) bool { return model != "gpt-5.6-sol" }
	rq := req("th", plus, pro)
	rq.ModelID = "gpt-5.6-sol"

	res, err := newRouter(t, newStore(), first).Assign(context.Background(), rq)

	require.NoError(t, err)
	assert.Equal(t, "pro", res.Account.AccountID)

	// And a thread already on the account that cannot serve it moves.
	s := newStore()
	s.assign("th2", "claude", "plus")
	rq.ThreadID = "th2"
	res, err = newRouter(t, s, first).Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "pro", res.Account.AccountID)
	assert.Equal(t, ReasonUnserved, res.Reason)
}

func TestStickyThreadLeavesForTheReasonItsAccountBecameUnusable(t *testing.T) {
	limited := acct("a", 0)
	limited.Status = models.AccountLimited
	limited.LimitedUntil = t0.Add(time.Hour)
	healthy := acct("b", 1)

	cases := []struct {
		name     string
		accounts []models.AccountConfig
		reason   string
		previous string
	}{
		{"limited", []models.AccountConfig{limited, healthy}, ReasonLimited, "label-a"},
		{"removed", []models.AccountConfig{healthy}, ReasonRemoved, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStore()
			s.assign("th", "claude", "a")

			res, err := newRouter(t, s, first).Assign(context.Background(), req("th", c.accounts...))

			require.NoError(t, err)
			assert.Equal(t, "b", res.Account.AccountID)
			assert.Equal(t, c.reason, res.Reason)
			assert.True(t, res.Changed)
			require.NotNil(t, res.Previous)
			assert.Equal(t, "a", res.Previous.AccountID)
			assert.Equal(t, c.previous, res.Previous.AccountLabel)
			assert.Equal(t, "b", s.assignments["th|claude"].AccountID)
			assert.Equal(t, c.reason, s.assignments["th|claude"].Reason)
		})
	}
}

func TestThreadAssignmentsAreKeyedByProvider(t *testing.T) {
	s := newStore()
	s.assign("th", "codex", "x")
	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID)
	assert.Equal(t, ReasonInitial, res.Reason)
	assert.Equal(t, "x", s.assignments["th|codex"].AccountID)
}

func TestCASLoserAdoptsTheWinner(t *testing.T) {
	s := newStore()
	s.put(usage("a", window("5h", 0.0, time.Hour, 5*time.Hour)))
	s.put(usage("b", window("5h", 0.9, time.Hour, 5*time.Hour)))
	s.beforeSet = func(s *fakeStore, a Assignment) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.beforeSet = nil
		s.assignments[s.key(a.ThreadID, a.Provider)] = Assignment{ThreadID: a.ThreadID, Provider: a.Provider, AccountID: "b", AssignedAt: t0}
	}

	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))

	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.AccountID, "the router wanted a but another activity assigned b first")
	assert.False(t, res.Changed, "the winner announces the move, not the loser")
	assert.Equal(t, "b", s.assignments["th|claude"].AccountID)
}

func TestCASLoserPicksAgainWhenTheWinnerIsNotUsable(t *testing.T) {
	s := newStore()
	s.put(benched("b", t0.Add(time.Hour), "5h"))
	s.beforeSet = func(s *fakeStore, a Assignment) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.beforeSet = nil
		s.assignments[s.key(a.ThreadID, a.Provider)] = Assignment{ThreadID: a.ThreadID, Provider: a.Provider, AccountID: "b", AssignedAt: t0}
	}

	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))

	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID)
	assert.True(t, res.Changed)
	assert.Equal(t, ReasonLimited, res.Reason)
	assert.Equal(t, "a", s.assignments["th|claude"].AccountID)
}

func TestWithoutAThreadNothingIsReadOrWritten(t *testing.T) {
	s := newStore()
	rq := req("", acct("a", 0), acct("b", 1))
	res, err := newRouter(t, s, first).Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID)
	assert.Zero(t, s.sets)
	assert.Empty(t, s.assignments)
}

func TestStoreFailuresDoNotStopTheConversation(t *testing.T) {
	boom := errors.New("db down")
	cases := map[string]func(s *fakeStore){
		"limits": func(s *fakeStore) { s.limitsErr = boom },
		"recent": func(s *fakeStore) { s.recentErr = boom },
		"set":    func(s *fakeStore) { s.setErr = boom },
		"get":    func(s *fakeStore) { s.getErr = boom },
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore()
			fail(s)
			res, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))
			require.NoError(t, err)
			assert.NotEmpty(t, res.Account.AccountID)
		})
	}
}

func TestStoreReadFailureNeverOverwritesAStoredAssignment(t *testing.T) {
	s := newStore()
	s.assign("th", "claude", "b")
	s.getErr = errors.New("db down")

	_, err := newRouter(t, s, first).Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))

	require.NoError(t, err)
	assert.Zero(t, s.sets, "an unreadable assignment is not replaced by a guess")
}

func TestNilStoreRoutesByPriorityOrder(t *testing.T) {
	r := New(Deps{Now: func() time.Time { return t0 }, Rand: first})
	res, err := r.Assign(context.Background(), req("th", acct("a", 0), acct("b", 1)))
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.AccountID)
}

func TestAlternatives(t *testing.T) {
	s := newStore()
	r := newRouter(t, s, first)
	ctx := context.Background()

	t.Run("another account is eligible", func(t *testing.T) {
		ok, reset, err := r.Alternatives(ctx, req("th", acct("a", 0), acct("b", 1)), "a")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, reset.IsZero())
	})

	t.Run("the only other account is limited", func(t *testing.T) {
		s.limits = map[string]LimitState{"b": benched("b", t0.Add(2*time.Hour), "7d")}
		ok, reset, err := r.Alternatives(ctx, req("th", acct("a", 0), acct("b", 1)), "a")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, t0.Add(2*time.Hour), reset)
	})

	t.Run("the excluded account counts as limited even before its bench is stored", func(t *testing.T) {
		s.limits = map[string]LimitState{"a": benched("a", t0.Add(time.Hour), "5h"), "b": benched("b", t0.Add(2*time.Hour), "7d")}
		ok, reset, err := r.Alternatives(ctx, req("th", acct("a", 0), acct("b", 1)), "a")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, t0.Add(time.Hour), reset, "earliest across all the driver's accounts, the excluded one included")
	})

	t.Run("excluded account with no stored bench", func(t *testing.T) {
		s.limits = map[string]LimitState{}
		ok, reset, err := r.Alternatives(ctx, req("th", acct("a", 0)), "a")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.True(t, reset.IsZero(), "no reset is known")
	})

	t.Run("accounts that cannot serve the model are not alternatives", func(t *testing.T) {
		other := acct("b", 1)
		other.Serves = func(string) bool { return false }
		ok, _, err := r.Alternatives(ctx, req("th", acct("a", 0), other), "a")
		require.NoError(t, err)
		assert.False(t, ok)
	})
}
