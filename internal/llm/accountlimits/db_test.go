// Copyright (c) 2025 Reliant Labs

package accountlimits_test

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

func upsert(t *testing.T, repo *db.Repo, user, provider, name string) core.ProviderAccount {
	t.Helper()
	identity := core.ClaudeIdentityKey(name, "org")
	if provider == "codex" {
		identity = core.CodexIdentityKey(name, "u")
	}
	a, _, err := repo.UpsertProviderAccount(context.Background(), core.AccountUpsert{
		UserID: user, Provider: provider, AuthKind: core.AuthKindOAuth, IdentityKey: identity,
		Email: name + "@example.com", ExternalAccountID: name,
		Secrets: core.AccountSecrets{Secret: "access-" + name, Refresh: "refresh-" + name}, ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	return a
}

func fptr(f float64) *float64 { return &f }

func snapshot(at time.Time, status ratelimit.Status, used float64) ratelimit.Snapshot {
	reset := at.Truncate(time.Hour).Add(3 * time.Hour)
	return ratelimit.Snapshot{Status: status, ObservedAt: at, RepresentativeWindow: "5h", Windows: []ratelimit.Window{
		{Name: "5h", UsedFraction: fptr(used), ResetsAt: reset, Length: 5 * time.Hour}}}
}

func TestDBTwoRecordersNeverRegressAnAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "u1", "claude", "alice")

	now := time.Now().UTC().Truncate(time.Millisecond)
	r1 := accountlimits.NewRecorder(repo, time.Now)
	r2 := accountlimits.NewRecorder(repo, time.Now)

	// Worker 2 saw the NEWER response; worker 1's older write arrives after it.
	r2.For("u1", a.ID).ObserveRateLimits(ctx, snapshot(now, ratelimit.StatusAllowed, 0.70))
	r2.Close()
	r1.For("u1", a.ID).ObserveRateLimits(ctx, snapshot(now.Add(-time.Minute), ratelimit.StatusAllowed, 0.30))
	r1.Close()

	got, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	l := got[a.ID]
	require.Len(t, l.Windows, 1)
	assert.InDelta(t, 0.70, *l.Windows[0].UsedFraction, 1e-9, "the older snapshot must not overwrite the newer one")
	assert.Equal(t, accountlimits.SourceHeaders, l.Source)
	assert.False(t, l.LastTrafficAt.IsZero(), "a header snapshot marks traffic")
	require.NotNil(t, l.UsageAPINextAt, "and schedules a poll for when its headers go stale")
	assert.WithinDuration(t, now.Add(10*time.Minute), *l.UsageAPINextAt, time.Second)
}

func TestDBRejectedSnapshotBenchesTheAccountAndAnAllowedOneLiftsIt(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "u1", "claude", "alice")
	rec := accountlimits.NewRecorder(repo, time.Now)
	defer rec.Close()

	now := time.Now().UTC()
	rej := snapshot(now, ratelimit.StatusRejected, 1)
	rec.For("u1", a.ID).ObserveRateLimits(ctx, rej)
	require.Eventually(t, func() bool {
		l, _ := repo.ListAccountLimits(ctx, "u1")
		return l[a.ID].LimitedUntil != nil
	}, 10*time.Second, 20*time.Millisecond, "a rejected snapshot is written immediately, not on the heartbeat")

	l, _ := repo.ListAccountLimits(ctx, "u1")
	assert.WithinDuration(t, rej.Windows[0].ResetsAt, *l[a.ID].LimitedUntil, time.Second)
	assert.Equal(t, "five_hour", l[a.ID].LimitedWindow)
	assert.Equal(t, core.AccountLimited, a.Status(ptr(l[a.ID]), now))

	// The provider is plainly serving again (an early reset): the bench lifts.
	rec.For("u1", a.ID).ObserveRateLimits(ctx, snapshot(now.Add(time.Second), ratelimit.StatusAllowed, 0.05))
	require.Eventually(t, func() bool {
		l, _ := repo.ListAccountLimits(ctx, "u1")
		return l[a.ID].LimitedUntil == nil
	}, 10*time.Second, 20*time.Millisecond)
}

func ptr[T any](v T) *T { return &v }

func TestDBBenchReturnsTheEffectiveUntilAndSchedulesAConfirmation(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "u1", "codex", "bob")
	rec := accountlimits.NewRecorder(repo, time.Now)
	defer rec.Close()

	// A limits row has to exist for the confirmation poll to be scheduled onto: Bench creates it.
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	got, err := rec.Bench(ctx, "u1", a.ID, reset, "seven_day")
	require.NoError(t, err)
	assert.True(t, got.Equal(reset))

	l, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, l[a.ID].LimitedUntil.Equal(reset))
	require.NotNil(t, l[a.ID].UsageAPINextAt)
	assert.True(t, l[a.ID].UsageAPINextAt.Equal(reset.Add(30*time.Second)))

	// An unknown reset benches for the first probe rung.
	b := upsert(t, repo, "u1", "codex", "carol")
	got, err = rec.Bench(ctx, "u1", b.ID, time.Time{}, "")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), got, 5*time.Second)
	l, _ = repo.ListAccountLimits(ctx, "u1")
	assert.Equal(t, 1, l[b.ID].LimitedProbes)

	_, err = rec.Bench(ctx, "u1", "no-such-account", time.Time{}, "")
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
}

func TestDBEnsureUsagePollScheduledOnlyFillsAnEmptySlotForPollableProviders(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	claude := upsert(t, repo, "u1", "claude", "alice")
	key, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{UserID: "u1", Provider: "anthropic", AuthKind: core.AuthKindAPIKey,
		IdentityKey: core.APIKeyIdentityKey("u1", "anthropic", "sk-ant-1"), KeyHint: "t-1", Secrets: core.AccountSecrets{Secret: "sk-ant-1"}})
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{claude.ID, key.ID} { // a limits row exists once anything has observed the account
		require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, core.LimitState{AccountID: id, UserID: "u1", Status: "allowed", ObservedAt: now}))
	}

	pollable := []string{"claude", "codex"}
	require.NoError(t, repo.EnsureUsagePollScheduled(ctx, claude.ID, now.Add(10*time.Minute), pollable))
	require.NoError(t, repo.EnsureUsagePollScheduled(ctx, key.ID, now.Add(10*time.Minute), pollable))
	l, _ := repo.ListAccountLimits(ctx, "u1")
	require.NotNil(t, l[claude.ID].UsageAPINextAt)
	assert.Nil(t, l[key.ID].UsageAPINextAt, "an API key has no usage API to poll")

	// A schedule already in place (say, a backoff) is never pulled forward by traffic.
	later := now.Add(6 * time.Hour)
	require.NoError(t, repo.ScheduleUsagePoll(ctx, claude.ID, later, 4, "429"))
	require.NoError(t, repo.EnsureUsagePollScheduled(ctx, claude.ID, now.Add(time.Minute), pollable))
	l, _ = repo.ListAccountLimits(ctx, "u1")
	assert.True(t, l[claude.ID].UsageAPINextAt.Equal(later))
	assert.Equal(t, 4, l[claude.ID].UsageAPIFailures, "and the failure count survives")
}

func TestDBClaimUsageFetchIsAtomicAcrossCallers(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "u1", "claude", "alice")
	now := time.Now().UTC()

	var granted atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.ClaimUsageFetch(ctx, a.ID, now, time.Minute)
			assert.NoError(t, err)
			if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, granted.Load(), "eight replicas, one provider call per minute")

	ok, err := repo.ClaimUsageFetch(ctx, a.ID, now.Add(61*time.Second), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = repo.ClaimUsageFetch(ctx, "ghost", now, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)
}

// scriptedDoer answers every usage request and counts it.
type scriptedDoer struct {
	mu    sync.Mutex
	calls map[string]int // by bearer token
	body  func() string
}

func (d *scriptedDoer) Do(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls == nil {
		d.calls = map[string]int{}
	}
	d.calls[r.Header.Get("Authorization")]++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: readCloser(d.body())}, nil
}

func TestDBTwoPollersNeverDoubleFetchAnAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	const n = 24
	now := time.Now().UTC()
	for i := range n {
		a := upsert(t, repo, "u1", "claude", fmt.Sprintf("acct%02d", i))
		require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, now.Add(-time.Duration(i+1)*time.Second), 0, ""))
	}

	doer := &scriptedDoer{body: func() string {
		return fmt.Sprintf(`{"five_hour":{"utilization":10,"resets_at":%q},"seven_day":{"utilization":20,"resets_at":%q}}`,
			now.Add(2*time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339))
	}}
	mk := func() *accountlimits.Poller {
		return &accountlimits.Poller{
			Store: repo,
			Fetcher: &accountlimits.Fetcher{Store: repo, Doer: doer,
				Tokens: accountlimits.StoredTokens{Store: repo}},
			Batch: 10, Concurrency: 4,
		}
	}
	p1, p2 := mk(), mk()

	var wg sync.WaitGroup
	handled := make([]int, 2)
	for i, p := range []*accountlimits.Poller{p1, p2} {
		wg.Add(1)
		go func() { defer wg.Done(); handled[i] = p.Tick(ctx) }()
	}
	wg.Wait()
	// Drain the remainder so every account is handled exactly once overall.
	for p1.Tick(ctx) > 0 {
	}

	var tokens []string
	for tok, c := range doer.calls {
		assert.Equal(t, 1, c, "account %s fetched %d times", tok, c)
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)
	assert.Len(t, tokens, n, "every due account was fetched")

	limits, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	for id, l := range limits {
		assert.Equal(t, accountlimits.SourceUsageAPI, l.Source, id)
		assert.Len(t, l.Windows, 2, id)
		assert.Nil(t, l.UsageAPINextAt, "never used and idle: not polled again")
	}
}

func TestDBPollerBacksOffAnUnavailableEndpointAndKeepsHeaderState(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "u1", "claude", "alice")
	now := time.Now().UTC().Truncate(time.Second)

	// Header state from real traffic, then a poll that the endpoint refuses.
	require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, core.LimitState{AccountID: a.ID, UserID: "u1", Status: "allowed",
		Source: accountlimits.SourceHeaders, ObservedAt: now.Add(-time.Hour),
		Windows: []core.LimitWindow{{Name: "5h", UsedFraction: fptr(0.4), ResetsAt: now.Add(time.Hour), LengthS: 5 * 3600}}}))
	require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, now.Add(-time.Second), 0, ""))

	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: readCloser("not found")}, nil
	})
	p := &accountlimits.Poller{Store: repo, Fetcher: &accountlimits.Fetcher{Store: repo, Doer: doer, Tokens: accountlimits.StoredTokens{Store: repo}}}
	require.Equal(t, 1, p.Tick(ctx))

	l, _ := repo.ListAccountLimits(ctx, "u1")
	assert.Equal(t, 1, l[a.ID].UsageAPIFailures)
	assert.Contains(t, l[a.ID].UsageAPIError, "404")
	require.NotNil(t, l[a.ID].UsageAPINextAt)
	assert.Greater(t, time.Until(*l[a.ID].UsageAPINextAt), 50*time.Minute)
	require.Len(t, l[a.ID].Windows, 1, "the header-derived windows survive a failed poll")
	assert.InDelta(t, 0.4, *l[a.ID].Windows[0].UsedFraction, 1e-9)
	assert.Equal(t, accountlimits.SourceHeaders, l[a.ID].Source)
	assert.Nil(t, l[a.ID].LimitedUntil)
	assert.Equal(t, 0, p.Tick(ctx), "backed off: not leased again")
}

func TestDBRefreshUsageEndToEnd(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "u1", "codex", "bob")
	now := time.Now().UTC()

	var calls atomic.Int32
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		assert.Equal(t, "bob", r.Header.Get("ChatGPT-Account-Id"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: readCloser(fmt.Sprintf(
			`{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":61,"limit_window_seconds":604800,"reset_at":%d}}}`,
			now.Add(time.Hour).Unix(), now.Add(48*time.Hour).Unix()))}, nil
	})
	f := &accountlimits.Fetcher{Store: repo, Doer: doer, Tokens: accountlimits.StoredTokens{Store: repo}}

	res, err := f.RefreshUsage(ctx, "u1", a.ID)
	require.NoError(t, err)
	assert.False(t, res.Throttled)
	require.NotNil(t, res.Limits)
	require.Len(t, res.Limits.Windows, 2)
	assert.InDelta(t, 0.25, *res.Limits.Windows[0].UsedFraction, 1e-9)

	again, err := f.RefreshUsage(ctx, "u1", a.ID)
	require.NoError(t, err)
	assert.True(t, again.Throttled)
	assert.EqualValues(t, 1, calls.Load())

	_, err = f.RefreshUsage(ctx, "u2", a.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
}

// Another user's refresh of an account id must leave the owner's poll schedule exactly as it was.
func TestDBRefreshByAnotherUserLeavesTheOwnersScheduleAlone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := upsert(t, repo, "owner", "claude", "alice")
	next := time.Now().UTC().Add(25 * time.Minute).Truncate(time.Millisecond)
	require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, next, 3, "429"))

	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		t.Error("the usage API must not be called for another user's account")
		return nil, fmt.Errorf("unexpected call")
	})
	f := &accountlimits.Fetcher{Store: repo, Doer: doer, Tokens: accountlimits.StoredTokens{Store: repo}}

	_, err := f.RefreshUsage(ctx, "intruder", a.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)

	l, err := repo.ListAccountLimits(ctx, "owner")
	require.NoError(t, err)
	require.NotNil(t, l[a.ID].UsageAPINextAt, "the owner's poll stays scheduled")
	assert.True(t, l[a.ID].UsageAPINextAt.Equal(next))
	assert.Equal(t, 3, l[a.ID].UsageAPIFailures)
	assert.Equal(t, "429", l[a.ID].UsageAPIError)
}

// The poller runs on the per-account refresh coordinator: for an account whose access token has
// expired it gets a refreshed token (and the rotation is persisted), where StoredTokens would
// have given up and left the account without polled numbers.
func TestDBPollerGetsARefreshedTokenForAnExpiredAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	a, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: "u1", Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey("alice", "org"), Email: "alice@example.com", ExternalAccountID: "alice",
		Secrets: core.AccountSecrets{Secret: "expired-access", Refresh: "refresh-1"}, ExpiresAt: now.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, now.Add(-time.Second), 0, ""))

	var exchanged atomic.Int32
	tokens := drivers.RefreshingTokens{Store: repo, Exchange: func(string) drivers.ProviderRefresh {
		return func(rt string) (core.AccountTokens, error) {
			exchanged.Add(1)
			assert.Equal(t, "refresh-1", rt)
			return core.AccountTokens{Secret: "refreshed-access", Refresh: "refresh-2", ExpiresAt: now.Add(8 * time.Hour)}, nil
		}
	}}

	var bearer atomic.Value
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		bearer.Store(r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: readCloser(fmt.Sprintf(
			`{"five_hour":{"utilization":10,"resets_at":%q},"seven_day":{"utilization":20,"resets_at":%q}}`,
			now.Add(2*time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339)))}, nil
	})
	p := &accountlimits.Poller{Store: repo, Fetcher: &accountlimits.Fetcher{Store: repo, Doer: doer, Tokens: tokens}}

	require.Equal(t, 1, p.Tick(ctx))

	assert.Equal(t, "Bearer refreshed-access", bearer.Load(), "the usage API is called with the refreshed token")
	assert.EqualValues(t, 1, exchanged.Load())
	stored, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
	require.NoError(t, err)
	assert.Equal(t, "refresh-2", stored.Secrets.Refresh, "the rotation went through the coordinator and is persisted")
	limits, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, accountlimits.SourceUsageAPI, limits[a.ID].Source)

	// A live token is used as it is: no second exchange.
	require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, time.Now().Add(-time.Second), 0, ""))
	_, err = (&accountlimits.Fetcher{Store: repo, Doer: doer, Tokens: tokens}).RefreshUsage(ctx, "u1", a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, exchanged.Load())
}
