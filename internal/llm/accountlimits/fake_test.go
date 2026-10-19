// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

var t0 = time.Now().UTC().Truncate(time.Second)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: t0} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeStore is an in-memory account store with the semantics the Postgres one
// documents.
type fakeStore struct {
	mu       sync.Mutex
	clock    *fakeClock
	accounts map[string]*core.AccountWithSecrets
	limits   map[string]*core.LimitState
	polledAt map[string]time.Time

	// upsertErr, when set, fails UpsertAccountLimitSnapshot.
	upsertErr error

	// slow, when set, blocks UpsertAccountLimitSnapshot until closed.
	slow chan struct{}

	upserts   int
	benches   []benchCall
	schedules []scheduleCall
	ensures   []time.Time
	claims    int
}

type benchCall struct {
	accountID string
	until     time.Time
	window    string
	probe     bool
}

type scheduleCall struct {
	accountID string
	next      time.Time
	failures  int
	err       string
}

func newFakeStore(c *fakeClock) *fakeStore {
	return &fakeStore{
		clock:    c,
		accounts: map[string]*core.AccountWithSecrets{},
		limits:   map[string]*core.LimitState{},
		polledAt: map[string]time.Time{},
	}
}

func (s *fakeStore) addAccount(id, user, provider string) *core.AccountWithSecrets {
	a := &core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: id, UserID: user, Provider: provider, AuthKind: core.AuthKindOAuth,
			ExternalAccountID: "ext-" + id, ExpiresAt: t0.Add(time.Hour)},
		Secrets: core.AccountSecrets{Secret: "tok-" + id},
	}
	s.accounts[id] = a
	return a
}

func (s *fakeStore) limit(id string) *core.LimitState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.limits[id]; l != nil {
		c := *l
		return &c
	}
	return nil
}

func (s *fakeStore) ensureRow(id string) *core.LimitState {
	l := s.limits[id]
	if l == nil {
		l = &core.LimitState{AccountID: id, UserID: s.accounts[id].UserID, Status: "unknown"}
		s.limits[id] = l
	}
	return l
}

func (s *fakeStore) GetAccountLimits(_ context.Context, id string) (*core.LimitState, error) {
	return s.limit(id), nil
}

func (s *fakeStore) GetProviderAccountWithSecrets(_ context.Context, userID, id string) (*core.AccountWithSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accounts[id]
	if a == nil || a.UserID != userID {
		return nil, core.ErrProviderAccountNotFound
	}
	c := *a
	return &c, nil
}

func (s *fakeStore) UpsertAccountLimitSnapshot(_ context.Context, st core.LimitState) error {
	if s.slow != nil {
		<-s.slow
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if s.accounts[st.AccountID] == nil {
		return nil
	}
	s.upserts++
	l := s.ensureRow(st.AccountID)
	if !st.ObservedAt.After(l.ObservedAt) {
		return nil
	}
	l.Status, l.Windows, l.RepresentativeWindow, l.Source, l.ObservedAt = st.Status, st.Windows, st.RepresentativeWindow, st.Source, st.ObservedAt
	if st.Source == SourceHeaders {
		l.LastTrafficAt = st.ObservedAt
	}
	if st.Status == "allowed" {
		l.LimitedUntil, l.LimitedWindow, l.LimitedProbes = nil, "", 0
	}
	return nil
}

func (s *fakeStore) BenchAccount(_ context.Context, userID, id string, until time.Time, window string, probe bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts[id] == nil {
		return core.ErrProviderAccountNotFound
	}
	s.benches = append(s.benches, benchCall{id, until, window, probe})
	l := s.ensureRow(id)
	if l.LimitedUntil == nil || until.After(*l.LimitedUntil) {
		u := until
		l.LimitedUntil, l.LimitedWindow = &u, window
	}
	if probe {
		l.LimitedProbes++
	} else {
		l.LimitedProbes = 0
	}
	return nil
}

func (s *fakeStore) EnsureUsagePollScheduled(_ context.Context, id string, next time.Time, providers []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accounts[id]
	if a == nil {
		return nil
	}
	ok := false
	for _, p := range providers {
		ok = ok || p == a.Provider
	}
	l := s.limits[id]
	if !ok || l == nil || l.UsageAPINextAt != nil {
		return nil
	}
	s.ensures = append(s.ensures, next)
	n := next
	l.UsageAPINextAt = &n
	return nil
}

func (s *fakeStore) ClaimUsageFetch(_ context.Context, id string, now time.Time, floor time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts[id] == nil {
		return false, nil
	}
	if last, ok := s.polledAt[id]; ok && last.After(now.Add(-floor)) {
		return false, nil
	}
	s.polledAt[id] = now
	s.claims++
	return true, nil
}

func (s *fakeStore) ScheduleUsagePoll(_ context.Context, id string, next time.Time, failures int, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts[id] == nil {
		return core.ErrProviderAccountNotFound
	}
	s.schedules = append(s.schedules, scheduleCall{id, next, failures, errText})
	l := s.ensureRow(id)
	if next.IsZero() {
		l.UsageAPINextAt = nil
	} else {
		n := next
		l.UsageAPINextAt = &n
	}
	l.UsageAPIFailures, l.UsageAPIError = failures, errText
	return nil
}

func (s *fakeStore) lastSchedule() scheduleCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules[len(s.schedules)-1]
}

// stubDoer answers usage-API requests from a script and records them.
type stubDoer struct {
	mu    sync.Mutex
	calls []*http.Request
	reply func(*http.Request) (*http.Response, error)
}

func (d *stubDoer) Do(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls = append(d.calls, r)
	d.mu.Unlock()
	return d.reply(r)
}

func (d *stubDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// staticTokens is a TokenSource.
type staticTokens struct {
	token string
	err   error
	asked []string
	mu    sync.Mutex
}

func (t *staticTokens) AccessToken(_ context.Context, _, accountID string) (string, error) {
	t.mu.Lock()
	t.asked = append(t.asked, accountID)
	t.mu.Unlock()
	return t.token, t.err
}
