// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

func resp(code int, body string, hdr ...string) (*http.Response, error) {
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Set(hdr[i], hdr[i+1])
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func claudeBody(used5h, used7d float64, now time.Time) string {
	return fmt.Sprintf(`{"five_hour":{"utilization":%g,"resets_at":%q},"seven_day":{"utilization":%g,"resets_at":%q}}`,
		used5h, now.Add(2*time.Hour).Format(time.RFC3339), used7d, now.Add(48*time.Hour).Format(time.RFC3339))
}

func codexBody(primary, secondary float64, now time.Time) string {
	return fmt.Sprintf(`{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,
		"primary_window":{"used_percent":%g,"limit_window_seconds":18000,"reset_at":%d},
		"secondary_window":{"used_percent":%g,"limit_window_seconds":604800,"reset_at":%d}}}`,
		primary, now.Add(2*time.Hour).Unix(), secondary, now.Add(48*time.Hour).Unix())
}

type fixture struct {
	clock  *fakeClock
	store  *fakeStore
	doer   *stubDoer
	tokens *staticTokens
	f      *Fetcher
}

func newFixture(t *testing.T, provider string) *fixture {
	t.Helper()
	c := newClock()
	st := newFakeStore(c)
	st.addAccount("a1", "u1", provider)
	st.ensureRow("a1")
	d := &stubDoer{reply: func(*http.Request) (*http.Response, error) { return resp(200, claudeBody(10, 20, c.Now())) }}
	tk := &staticTokens{token: "access-token-123"}
	return &fixture{clock: c, store: st, doer: d, tokens: tk,
		f: &Fetcher{Store: st, Tokens: tk, Doer: d, Now: c.Now}}
}

func TestFetchUsageStoresWindowsAndSchedulesTheNextPoll(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour) // used within the day, no recent headers

	res := fx.f.PollLeased(context.Background(), "a1")
	require.Equal(t, OutcomeUpdated, res.Outcome, "err=%v", res.Err)

	l := fx.store.limit("a1")
	assert.Equal(t, SourceUsageAPI, l.Source)
	require.Len(t, l.Windows, 2)
	assert.InDelta(t, 0.10, *l.Windows[0].UsedFraction, 1e-9)
	assert.Equal(t, 0, l.UsageAPIFailures)
	require.NotNil(t, l.UsageAPINextAt)
	assert.True(t, l.UsageAPINextAt.Equal(fx.clock.Now().Add(10*time.Minute)), "used in the last 24h: every 10 minutes")

	require.Equal(t, 1, fx.doer.count())
	req := fx.doer.calls[0]
	assert.Equal(t, "Bearer access-token-123", req.Header.Get("Authorization"))
	assert.Equal(t, "oauth-2025-04-20", req.Header.Get("anthropic-beta"))
	assert.Equal(t, "api.anthropic.com", req.URL.Host)
}

func TestFetchUsageCodexSendsTheChatGPTAccountID(t *testing.T) {
	fx := newFixture(t, "codex")
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(200, codexBody(25, 61, fx.clock.Now())) }

	res := fx.f.PollLeased(context.Background(), "a1")
	require.Equal(t, OutcomeUpdated, res.Outcome, "err=%v", res.Err)
	assert.Equal(t, "ext-a1", fx.doer.calls[0].Header.Get("ChatGPT-Account-Id"))
	assert.Equal(t, "chatgpt.com", fx.doer.calls[0].URL.Host)
	assert.Equal(t, "Bearer access-token-123", fx.doer.calls[0].Header.Get("Authorization"))
	l := fx.store.limit("a1")
	require.Len(t, l.Windows, 2)
	assert.Equal(t, "primary", l.Windows[0].Name)
}

func TestCadence(t *testing.T) {
	now := t0
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	future := func(d time.Duration) *time.Time { x := now.Add(d); return &x }

	cases := []struct {
		name      string
		limits    *core.LimitState
		wantCall  bool
		wantNext  time.Duration // from now, when not called
		afterNext time.Duration // from now, when called; 0 = unscheduled
	}{
		{name: "active with headers in the last 10 min is not polled",
			limits: &core.LimitState{LastTrafficAt: ago(3 * time.Minute)}, wantNext: 7 * time.Minute},
		{name: "used in the last 24h with stale headers: polled, then every 10 minutes",
			limits: &core.LimitState{LastTrafficAt: ago(2 * time.Hour)}, wantCall: true, afterNext: 10 * time.Minute},
		{name: "idle over 24h: polled once if it came due, then left alone",
			limits: &core.LimitState{LastTrafficAt: ago(30 * time.Hour)}, wantCall: true, afterNext: 0},
		{name: "never used: polled once, then left alone",
			limits: &core.LimitState{}, wantCall: true, afterNext: 0},
		{name: "limited: not polled, confirmed at reset + 30s",
			limits: &core.LimitState{LimitedUntil: future(2 * time.Hour), LastTrafficAt: ago(time.Hour)}, wantNext: 2*time.Hour + 30*time.Second},
		{name: "bench just ended: polled to confirm the reset",
			limits: &core.LimitState{LimitedUntil: future(-10 * time.Second), LastTrafficAt: ago(time.Hour)}, wantCall: true, afterNext: 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, "claude")
			l := tc.limits
			l.AccountID, l.UserID = "a1", "u1"
			fx.store.limits["a1"] = l

			res := fx.f.PollLeased(context.Background(), "a1")
			if !tc.wantCall {
				assert.Equal(t, OutcomeDeferred, res.Outcome)
				assert.Zero(t, fx.doer.count(), "no provider call")
				assert.True(t, fx.store.lastSchedule().next.Equal(now.Add(tc.wantNext)), "next=%v", fx.store.lastSchedule().next.Sub(now))
				return
			}
			require.Equal(t, OutcomeUpdated, res.Outcome, "err=%v", res.Err)
			assert.Equal(t, 1, fx.doer.count())
			if tc.afterNext == 0 {
				assert.Nil(t, fx.store.limit("a1").UsageAPINextAt)
			} else {
				require.NotNil(t, fx.store.limit("a1").UsageAPINextAt)
				assert.True(t, fx.store.limit("a1").UsageAPINextAt.Equal(now.Add(tc.afterNext)))
			}
		})
	}
}

func TestPausedAndNeedsSignInAccountsAreNotPolled(t *testing.T) {
	for name, mutate := range map[string]func(*core.AccountWithSecrets){
		"paused":        func(a *core.AccountWithSecrets) { x := t0; a.PausedAt = &x },
		"needs sign-in": func(a *core.AccountWithSecrets) { x := t0; a.NeedsReauthAt = &x },
	} {
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t, "claude")
			mutate(fx.store.accounts["a1"])
			res := fx.f.PollLeased(context.Background(), "a1")
			assert.Equal(t, OutcomeSkipped, res.Outcome)
			assert.Zero(t, fx.doer.count())
			assert.Nil(t, fx.store.limit("a1").UsageAPINextAt, "unscheduled")
		})
	}
}

func TestProviderWithoutAUsageAPIIsNeverPolled(t *testing.T) {
	fx := newFixture(t, "anthropic")
	res := fx.f.PollLeased(context.Background(), "a1")
	assert.Equal(t, OutcomeSkipped, res.Outcome)
	assert.Zero(t, fx.doer.count())
	assert.Nil(t, fx.store.limit("a1").UsageAPINextAt)
}

func TestDisabledEndpointFlagIsHonoured(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.f.Enabled = func(string) bool { return false }
	res := fx.f.PollLeased(context.Background(), "a1")
	assert.Equal(t, OutcomeSkipped, res.Outcome)
	assert.Zero(t, fx.doer.count(), "a disabled provider is never called")
	assert.Nil(t, fx.store.limit("a1").UsageAPINextAt)

	// On-demand refresh obeys it too: it answers the stored state.
	got, err := fx.f.RefreshUsage(context.Background(), "u1", "a1")
	require.NoError(t, err)
	assert.Zero(t, fx.doer.count())
	assert.Equal(t, "a1", got.Account.ID)
}

func TestUnavailableEndpointDegradesToHeadersOnlyAndBacksOffLong(t *testing.T) {
	for _, code := range []int{401, 403, 404} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			fx := newFixture(t, "claude")
			fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour)
			fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(code, `{"error":"nope"}`) }

			res := fx.f.PollLeased(context.Background(), "a1")
			require.Equal(t, OutcomeFailed, res.Outcome)

			l := fx.store.limit("a1")
			assert.Equal(t, 1, l.UsageAPIFailures)
			assert.Contains(t, l.UsageAPIError, strconv.Itoa(code))
			assert.Contains(t, l.UsageAPIError, "headers only")
			require.NotNil(t, l.UsageAPINextAt)
			assert.GreaterOrEqual(t, l.UsageAPINextAt.Sub(fx.clock.Now()), time.Hour, "long backoff")

			// It must not bench the account, mark it needing sign-in, or change its windows.
			assert.Empty(t, fx.store.benches)
			assert.Nil(t, fx.store.accounts["a1"].NeedsReauthAt)
			assert.Zero(t, fx.store.upserts)

			// Backoff doubles per consecutive failure up to a ceiling.
			var last time.Duration
			for i := 0; i < 8; i++ {
				fx.clock.Advance(13 * time.Hour)
				fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour) // still in use
				fx.f.PollLeased(context.Background(), "a1")
				last = fx.store.limit("a1").UsageAPINextAt.Sub(fx.clock.Now())
			}
			assert.Equal(t, 12*time.Hour, last)
		})
	}
}

func TestThrottledEndpointBacksOffAndNeverBenchesTheAccount(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour)
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(429, `{}`, "Retry-After", "0") }

	var delays []time.Duration
	for i := 0; i < 8; i++ {
		fx.clock.Advance(2 * time.Hour)
		res := fx.f.PollLeased(context.Background(), "a1")
		require.Equal(t, OutcomeFailed, res.Outcome)
		delays = append(delays, fx.store.limit("a1").UsageAPINextAt.Sub(fx.clock.Now()))
	}
	assert.Equal(t, []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, time.Hour, time.Hour}, delays, "1 → 2 → 4 … → 60 min")
	assert.Empty(t, fx.store.benches, "a 429 from the usage endpoint backs the poll off; it never benches the account")
	assert.Equal(t, 8, fx.store.limit("a1").UsageAPIFailures)
}

func TestThrottledHonoursALongerRetryAfter(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour)
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(429, `{}`, "Retry-After", "5400") }
	fx.f.PollLeased(context.Background(), "a1")
	assert.Equal(t, 90*time.Minute, fx.store.limit("a1").UsageAPINextAt.Sub(fx.clock.Now()))
}

func TestUnreadableBodyDegradesToHeadersOnly(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(200, `<html>login</html>`) }
	res := fx.f.PollLeased(context.Background(), "a1")
	require.Equal(t, OutcomeFailed, res.Outcome)
	l := fx.store.limit("a1")
	assert.Contains(t, l.UsageAPIError, "unknown format")
	assert.GreaterOrEqual(t, l.UsageAPINextAt.Sub(fx.clock.Now()), time.Hour)
	assert.Zero(t, fx.store.upserts)
}

func TestNetworkErrorBacksOffShortly(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: connection refused") }
	res := fx.f.PollLeased(context.Background(), "a1")
	require.Equal(t, OutcomeFailed, res.Outcome)
	assert.Equal(t, time.Minute, fx.store.limit("a1").UsageAPINextAt.Sub(fx.clock.Now()))
}

func TestUsageFailuresDoNotSpamLogsAndNeverLeakTheToken(t *testing.T) {
	var buf bytes.Buffer
	prev := logging.GetOutput()
	logging.SetOutput(&buf)
	defer logging.SetOutput(prev)

	fx := newFixture(t, "claude")
	fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour)
	fx.doer.reply = func(*http.Request) (*http.Response, error) {
		return resp(403, `{"error":"forbidden access-token-123"}`)
	}
	for i := 0; i < 20; i++ {
		fx.clock.Advance(13 * time.Hour)
		fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour)
		fx.f.PollLeased(context.Background(), "a1")
	}
	assert.NotContains(t, buf.String(), "access-token-123")
	assert.LessOrEqual(t, strings.Count(buf.String(), "usage API read failed"), 1, "one warning when reads start failing, then quiet")
}

func TestExhaustedUsageWindowBenchesTheAccount(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(200, claudeBody(100, 40, fx.clock.Now())) }

	res := fx.f.PollLeased(context.Background(), "a1")
	require.Equal(t, OutcomeUpdated, res.Outcome)

	l := fx.store.limit("a1")
	require.NotNil(t, l.LimitedUntil, "a spent 5h window benches the account until its reset")
	assert.True(t, l.LimitedUntil.Equal(fx.clock.Now().Add(2*time.Hour).Truncate(time.Second)))
	assert.Equal(t, "five_hour", l.LimitedWindow)
	assert.True(t, l.UsageAPINextAt.Equal(l.LimitedUntil.Add(30*time.Second)), "confirm 30s after the reset")
}

func TestUsageReadingDoesNotLiftABenchTheProviderAnnounced(t *testing.T) {
	fx := newFixture(t, "claude")
	until := fx.clock.Now().Add(3 * time.Hour)
	fx.store.limits["a1"].LimitedUntil, fx.store.limits["a1"].LimitedWindow = &until, "seven_day"
	fx.store.limits["a1"].UsageAPINextAt = nil
	fx.store.limits["a1"].LastTrafficAt = fx.clock.Now().Add(-time.Hour)
	// A bench in force makes a leased poll defer, so refresh on demand: it reads regardless.
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(200, claudeBody(10, 20, fx.clock.Now())) }

	_, err := fx.f.RefreshUsage(context.Background(), "u1", "a1")
	require.NoError(t, err)
	l := fx.store.limit("a1")
	require.NotNil(t, l.LimitedUntil, "an 'allowed' reading cannot see a model-scoped refusal; the provider's own bench stands")
	assert.True(t, l.LimitedUntil.Equal(until))
}

func TestOnDemandRefreshIsThrottledAt60Seconds(t *testing.T) {
	fx := newFixture(t, "claude")
	ctx := context.Background()

	r1, err := fx.f.RefreshUsage(ctx, "u1", "a1")
	require.NoError(t, err)
	assert.False(t, r1.Throttled)
	require.NotNil(t, r1.Limits)
	assert.Len(t, r1.Limits.Windows, 2)
	require.Equal(t, 1, fx.doer.count())

	fx.clock.Advance(59 * time.Second)
	r2, err := fx.f.RefreshUsage(ctx, "u1", "a1")
	require.NoError(t, err)
	assert.True(t, r2.Throttled, "inside the floor the stored state comes back")
	assert.Len(t, r2.Limits.Windows, 2)
	assert.Equal(t, 1, fx.doer.count(), "no second provider call")

	fx.clock.Advance(2 * time.Second)
	r3, err := fx.f.RefreshUsage(ctx, "u1", "a1")
	require.NoError(t, err)
	assert.False(t, r3.Throttled)
	assert.Equal(t, 2, fx.doer.count())
}

func TestOnDemandRefreshAndPollerShareTheFloor(t *testing.T) {
	fx := newFixture(t, "claude")
	_, err := fx.f.RefreshUsage(context.Background(), "u1", "a1")
	require.NoError(t, err)
	fx.clock.Advance(10 * time.Second)
	res := fx.f.PollLeased(context.Background(), "a1")
	assert.Equal(t, OutcomeThrottled, res.Outcome)
	assert.Equal(t, 1, fx.doer.count())
	assert.True(t, fx.store.lastSchedule().next.After(fx.clock.Now()), "rescheduled, not dropped")
}

func TestOnDemandRefreshOfAFailingEndpointIsNotAnError(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(403, `{}`) }
	got, err := fx.f.RefreshUsage(context.Background(), "u1", "a1")
	require.NoError(t, err, "a refused usage endpoint is not a failed RPC")
	assert.False(t, got.Throttled)
	require.NotNil(t, got.Limits)
	assert.Contains(t, got.Limits.UsageAPIError, "403")
}

func TestOnDemandRefreshOfAnotherUsersAccountIsNotFound(t *testing.T) {
	fx := newFixture(t, "claude")
	_, err := fx.f.RefreshUsage(context.Background(), "someone-else", "a1")
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = fx.f.RefreshUsage(context.Background(), "u1", "ghost")
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	assert.Zero(t, fx.doer.count())
}

func TestRefreshOfAnotherUsersAccountLeavesTheOwnersScheduleAlone(t *testing.T) {
	fx := newFixture(t, "claude")
	next := fx.clock.Now().Add(10 * time.Minute)
	require.NoError(t, fx.store.ScheduleUsagePoll(context.Background(), "a1", next, 2, "earlier failure"))
	before := len(fx.store.schedules)

	_, err := fx.f.RefreshUsage(context.Background(), "someone-else", "a1")
	require.ErrorIs(t, err, core.ErrProviderAccountNotFound)

	assert.Len(t, fx.store.schedules, before, "a foreign refresh must not write the schedule")
	l := fx.store.limit("a1")
	require.NotNil(t, l.UsageAPINextAt, "the owner's poll must stay scheduled")
	assert.True(t, l.UsageAPINextAt.Equal(next))
	assert.Equal(t, 2, l.UsageAPIFailures)
	assert.Zero(t, fx.doer.count())
}

func TestTokenComesFromTheTokenSourceForTheRightAccount(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.tokens.token = "freshly-refreshed"
	_, err := fx.f.RefreshUsage(context.Background(), "u1", "a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a1"}, fx.tokens.asked)
	assert.Equal(t, "Bearer freshly-refreshed", fx.doer.calls[0].Header.Get("Authorization"))
}

func TestTokenSourceFailureBacksOffAndCallsNothing(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.tokens.err = errors.New("refresh failed: invalid_grant")
	res := fx.f.PollLeased(context.Background(), "a1")
	require.Equal(t, OutcomeFailed, res.Outcome)
	assert.Zero(t, fx.doer.count())
	assert.Contains(t, fx.store.limit("a1").UsageAPIError, "no usable access token")
	assert.Nil(t, fx.store.accounts["a1"].NeedsReauthAt, "marking an account needing sign-in is the refresh coordinator's call, never the poller's")
}

func TestIdleAccountWhoseReadsKeepFailingStopsBeingPolled(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.doer.reply = func(*http.Request) (*http.Response, error) { return resp(404, `{}`) }
	for i := 0; i < 3; i++ {
		fx.clock.Advance(13 * time.Hour)
		fx.f.PollLeased(context.Background(), "a1")
	}
	assert.Nil(t, fx.store.limit("a1").UsageAPINextAt)
	assert.Equal(t, 3, fx.store.limit("a1").UsageAPIFailures)
}

func TestDeletedAccountIsDroppedFromTheSchedule(t *testing.T) {
	fx := newFixture(t, "claude")
	delete(fx.store.accounts, "a1")
	res := fx.f.PollLeased(context.Background(), "a1")
	assert.Equal(t, OutcomeSkipped, res.Outcome)
	assert.Zero(t, fx.doer.count())
}

func TestStoredTokensRefusesAnExpiredToken(t *testing.T) {
	fx := newFixture(t, "claude")
	src := StoredTokens{Store: fx.store, Now: fx.clock.Now}
	tok, err := src.AccessToken(context.Background(), "u1", "a1")
	require.NoError(t, err)
	assert.Equal(t, "tok-a1", tok)

	fx.clock.Advance(2 * time.Hour)
	_, err = src.AccessToken(context.Background(), "u1", "a1")
	assert.ErrorIs(t, err, ErrTokenExpired)
}

// ---- poller ----

type fakeLeaser struct {
	store *fakeStore
}

func (l *fakeLeaser) LeaseUsagePolls(_ context.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	var ids []string
	for id, lim := range l.store.limits {
		if lim.UsageAPINextAt != nil && !lim.UsageAPINextAt.After(now) && len(ids) < limit {
			ids = append(ids, id)
			n := now.Add(lease)
			lim.UsageAPINextAt = &n
		}
	}
	return ids, nil
}

func TestPollerTickPollsLeasedAccountsOnly(t *testing.T) {
	fx := newFixture(t, "claude")
	fx.store.addAccount("a2", "u1", "claude")
	fx.store.addAccount("a3", "u1", "claude")
	for _, id := range []string{"a1", "a2"} {
		fx.store.ensureRow(id)
		due := fx.clock.Now().Add(-time.Second)
		fx.store.limits[id].UsageAPINextAt = &due
	}
	fx.store.ensureRow("a3") // not scheduled

	p := &Poller{Store: &fakeLeaser{store: fx.store}, Fetcher: fx.f, Now: fx.clock.Now}
	assert.Equal(t, 2, p.Tick(context.Background()))
	assert.Equal(t, 2, fx.doer.count())
	assert.Equal(t, 0, p.Tick(context.Background()), "leased accounts are not leased again")
}

func TestPollerSurvivesAPanickingPoll(t *testing.T) {
	fx := newFixture(t, "claude")
	due := fx.clock.Now().Add(-time.Second)
	fx.store.limits["a1"].UsageAPINextAt = &due
	fx.doer.reply = func(*http.Request) (*http.Response, error) { panic("boom") }
	p := &Poller{Store: &fakeLeaser{store: fx.store}, Fetcher: fx.f, Now: fx.clock.Now}
	assert.NotPanics(t, func() { p.Tick(context.Background()) })
}

func TestPollerRunStopsWithItsContext(t *testing.T) {
	fx := newFixture(t, "claude")
	p := &Poller{Store: &fakeLeaser{store: fx.store}, Fetcher: fx.f, Every: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
