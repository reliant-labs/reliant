package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// issuer is a fake OAuth issuer whose refresh tokens ROTATE: each is good
// once, so a second redemption of the same token is rejected — exactly what
// makes a double refresh across replicas sign a person out.
type issuer struct {
	clk   *clock
	life  time.Duration
	mu    sync.Mutex
	live  map[string]bool
	n     int
	calls atomic.Int32
	// fail, when set, is returned instead of redeeming.
	fail error
	// delay is how long a redemption takes.
	delay time.Duration
}

func newIssuer(clk *clock) *issuer {
	return &issuer{clk: clk, life: time.Hour, live: map[string]bool{}}
}

// mint returns a fresh token set, its refresh token registered as live.
func (is *issuer) mint() Tokens {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.n++
	rt := fmt.Sprintf("rt-%d", is.n)
	is.live[rt] = true
	return Tokens{AccessToken: fmt.Sprintf("at-%d", is.n), RefreshToken: rt, IDToken: fmt.Sprintf("id-%d", is.n),
		Expiry: is.clk.Now().Add(is.life)}
}

func (is *issuer) Refresh(ctx context.Context, rt string) (Tokens, error) {
	is.calls.Add(1)
	if is.delay > 0 {
		select {
		case <-time.After(is.delay):
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		}
	}
	is.mu.Lock()
	if is.fail != nil {
		err := is.fail
		is.mu.Unlock()
		return Tokens{}, err
	}
	if !is.live[rt] {
		is.mu.Unlock()
		return Tokens{}, fmt.Errorf("invalid_grant: %w", ErrRefreshRejected)
	}
	delete(is.live, rt)
	is.mu.Unlock()
	t := is.mint()
	t.IDToken = "" // an issuer may omit it on refresh: the old one is kept
	return t, nil
}

func (is *issuer) setFail(err error) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.fail = err
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestManager(t *testing.T, store Store, clk *clock, r Refresher, mod func(*Config)) *Manager {
	t.Helper()
	cfg := Config{Store: store, Refresher: r, Now: clk.Now, Logger: quiet()}
	if mod != nil {
		mod(&cfg)
	}
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustCreate(t *testing.T, m *Manager, tokens Tokens, subject string) Issued {
	t.Helper()
	is, err := m.Create(context.Background(), tokens, Meta{Subject: subject, UserAgent: "test-agent"})
	if err != nil {
		t.Fatal(err)
	}
	return is
}

func TestNewIDShape(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !IsID(id) || !strings.HasPrefix(id, IDPrefix) {
		t.Fatalf("NewID() = %q, not an id", id)
	}
	other, _ := NewID()
	if other == id {
		t.Fatal("two ids are equal")
	}
	for _, s := range []string{"", IDPrefix, "eyJhbGciOiJSUzI1NiJ9.e30.sig", "fk_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		id[:len(id)-1], id + "A", strings.Replace(id, id[5:6], "*", 1), "FS_" + id[3:]} {
		if IsID(s) {
			t.Errorf("IsID(%q) = true", s)
		}
	}
}

func TestSealedTokensOpenOnlyUnderTheirOwnID(t *testing.T) {
	id, _ := NewID()
	k, err := deriveKeys(id)
	if err != nil {
		t.Fatal(err)
	}
	want := Tokens{AccessToken: "at", RefreshToken: "rt", IDToken: "it", Expiry: time.UnixMilli(1_700_000_000_000)}
	blob, err := seal(k, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := open(k, blob)
	if err != nil || got != want {
		t.Fatalf("open = %+v, %v; want %+v", got, err, want)
	}
	for _, secret := range []string{"at", "rt", "it", id} {
		if bytes.Contains(blob, []byte(secret)) && len(secret) > 2 {
			t.Errorf("sealed blob contains %q in the clear", secret)
		}
	}
	otherID, _ := NewID()
	ok, _ := deriveKeys(otherID)
	if _, err := open(ok, blob); !errors.Is(err, errCorrupt) {
		t.Errorf("open under another id: %v, want errCorrupt", err)
	}
	// The blob moved onto another row (same seal key, other lookup key) does not open.
	moved := keys{lookup: ok.lookup, seal: k.seal}
	if _, err := open(moved, blob); !errors.Is(err, errCorrupt) {
		t.Errorf("open on another row: %v, want errCorrupt", err)
	}
	if bytes.Equal(k.lookup, k.seal) {
		t.Error("lookup and seal keys are equal")
	}
}

func TestTokensRedactThemselves(t *testing.T) {
	tok := Tokens{AccessToken: "secret-access", RefreshToken: "secret-refresh"}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "tokens", tok)
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok), buf.String()} {
		if strings.Contains(s, "secret") {
			t.Errorf("tokens leaked: %s", s)
		}
	}
}

func TestStoreHoldsNoCredential(t *testing.T) {
	clk := newClock()
	store := NewMemoryStore()
	m := newTestManager(t, store, clk, nil, nil)
	tokens := Tokens{AccessToken: "access-token-value", RefreshToken: "refresh-token-value", IDToken: "id-token-value", Expiry: clk.Now().Add(time.Hour)}
	is := mustCreate(t, m, tokens, "u1")
	for _, r := range store.records {
		for _, secret := range []string{is.ID, is.ID[len(IDPrefix):], tokens.AccessToken, tokens.RefreshToken, tokens.IDToken} {
			if bytes.Contains(r.Key, []byte(secret)) || bytes.Contains(r.Sealed, []byte(secret)) {
				t.Errorf("the store holds %q in the clear", secret)
			}
		}
		if r.Subject != "u1" || r.UserAgent != "test-agent" {
			t.Errorf("record = %+v", r)
		}
	}
}

func TestAccessTokenRefreshesBeforeExpiryAndRotates(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	m := newTestManager(t, NewMemoryStore(), clk, is, nil)
	ctx := context.Background()
	s := mustCreate(t, m, is.mint(), "u1")

	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at-1" {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	if is.calls.Load() != 0 {
		t.Fatal("refreshed a fresh token")
	}
	// Within RefreshBefore of expiry: renewed, before the token dies.
	clk.Advance(time.Hour - time.Minute)
	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at-2" {
		t.Fatalf("AccessToken near expiry = %q, %v; want the renewed at-2", tok, err)
	}
	// The rotated refresh token was stored: the next renewal redeems rt-2
	// (the issuer refuses rt-1 now) and succeeds.
	clk.Advance(time.Hour)
	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at-3" {
		t.Fatalf("second renewal = %q, %v; want at-3 (the rotated refresh token must be kept)", tok, err)
	}
	if n := is.calls.Load(); n != 2 {
		t.Errorf("refresher called %d times, want 2", n)
	}
	// An issuer that omits the ID token on refresh does not erase it.
	tokens, _, err := m.Revoke(ctx, s.ID)
	if err != nil || tokens.IDToken != "id-1" || tokens.RefreshToken != "rt-3" {
		t.Errorf("stored tokens after renewals: %+v, %v", tokens, err)
	}
}

func TestOneRefreshPerSessionAcrossRequestsAndReplicas(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	is.delay = 20 * time.Millisecond
	store := NewMemoryStore()
	// Two replicas over one store, caches off so every call reaches it.
	a := newTestManager(t, store, clk, is, func(c *Config) { c.CacheTTL = -1 })
	b := newTestManager(t, store, clk, is, func(c *Config) { c.CacheTTL = -1 })
	s := mustCreate(t, a, is.mint(), "u1")
	clk.Advance(2 * time.Hour) // expired: every caller must wait for the renewal

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		m := a
		if i%2 == 1 {
			m = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := m.AccessToken(context.Background(), s.ID)
			if err == nil && tok != "at-2" {
				err = fmt.Errorf("token %q, want at-2", tok)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := is.calls.Load(); n != 1 {
		t.Errorf("40 concurrent requests on two replicas refreshed %d times, want 1 (a second redemption of a rotating token signs the person out)", n)
	}
}

func TestRejectedRefreshEndsTheSession(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	store := NewMemoryStore()
	m := newTestManager(t, store, clk, is, nil)
	tokens := is.mint()
	s := mustCreate(t, m, tokens, "u1")
	is.mu.Lock()
	delete(is.live, tokens.RefreshToken) // revoked at the issuer
	is.mu.Unlock()
	clk.Advance(2 * time.Hour)
	if _, err := m.AccessToken(context.Background(), s.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("AccessToken after a refused refresh: %v, want ErrNoSession", err)
	}
	if len(store.records) != 0 {
		t.Error("the ended session's row was kept")
	}
}

func TestUnreachableIssuerDoesNotSignAnyoneOut(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	store := NewMemoryStore()
	m := newTestManager(t, store, clk, is, func(c *Config) { c.CacheTTL = -1 })
	ctx := context.Background()
	s := mustCreate(t, m, is.mint(), "u1")
	is.setFail(errors.New("dial tcp: connection refused"))

	// Due for renewal but still valid: the current token keeps working.
	clk.Advance(time.Hour - time.Minute)
	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at-1" {
		t.Fatalf("still-valid token during an outage = %q, %v", tok, err)
	}
	// Expired during the outage: unavailable, NOT signed out.
	clk.Advance(2 * time.Minute)
	if _, err := m.AccessToken(ctx, s.ID); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoSession) {
		t.Fatalf("expired token during an outage: %v, want ErrUnavailable", err)
	}
	if len(store.records) != 1 {
		t.Fatal("an outage deleted the session")
	}
	// The issuer comes back: the session renews.
	is.setFail(nil)
	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at-2" {
		t.Fatalf("after the outage = %q, %v", tok, err)
	}
}

func TestNoRefreshTokenEndsWithTheAccessToken(t *testing.T) {
	clk := newClock()
	m := newTestManager(t, NewMemoryStore(), clk, newIssuer(clk), nil)
	ctx := context.Background()
	s := mustCreate(t, m, Tokens{AccessToken: "at", Expiry: clk.Now().Add(time.Hour)}, "u1")
	clk.Advance(time.Hour - time.Minute)
	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at" {
		t.Fatalf("before expiry = %q, %v", tok, err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := m.AccessToken(ctx, s.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after expiry with no refresh token: %v, want ErrNoSession", err)
	}
}

func TestLifetimeAndIdleTimeout(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	is.life = 30 * 24 * time.Hour // keep refreshes out of this test
	m := newTestManager(t, NewMemoryStore(), clk, is, func(c *Config) {
		c.Lifetime, c.IdleTimeout, c.CacheTTL = 10*24*time.Hour, 3*24*time.Hour, -1
	})
	ctx := context.Background()

	idle := mustCreate(t, m, is.mint(), "u1")
	clk.Advance(3*24*time.Hour + time.Second)
	if _, err := m.AccessToken(ctx, idle.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unused for longer than the idle timeout: %v, want ErrNoSession", err)
	}

	active := mustCreate(t, m, is.mint(), "u1")
	for i := 0; i < 9; i++ { // used every 2 days: idle never ends it
		clk.Advance(2 * 24 * time.Hour)
		_, err := m.AccessToken(ctx, active.ID)
		if i < 4 && err != nil {
			t.Fatalf("day %d: %v", 2*(i+1), err)
		}
		if i >= 5 && !errors.Is(err, ErrNoSession) {
			t.Fatalf("day %d, past the 10-day lifetime: %v, want ErrNoSession", 2*(i+1), err)
		}
	}
	if !active.ExpiresAt.Equal(newClock().Now().Add(3*24*time.Hour + time.Second).Add(10 * 24 * time.Hour)) {
		t.Errorf("ExpiresAt = %v", active.ExpiresAt)
	}
}

func TestRevokeTakesEffectAtOnceHereAndWithinTheCacheTTLElsewhere(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	store := NewMemoryStore()
	a := newTestManager(t, store, clk, is, nil)
	b := newTestManager(t, store, clk, is, nil)
	ctx := context.Background()
	s := mustCreate(t, a, is.mint(), "u1")
	if _, err := b.AccessToken(ctx, s.ID); err != nil { // b caches it
		t.Fatal(err)
	}
	tokens, info, err := a.Revoke(ctx, s.ID)
	if err != nil || tokens.RefreshToken != "rt-1" || info.Subject != "u1" {
		t.Fatalf("Revoke = %+v, %+v, %v", tokens, info, err)
	}
	if _, err := a.AccessToken(ctx, s.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("the revoking replica still accepts the session: %v", err)
	}
	if _, err := b.AccessToken(ctx, s.ID); err != nil {
		t.Errorf("another replica within its cache TTL: %v, want its cached token", err)
	}
	clk.Advance(DefaultCacheTTL)
	if _, err := b.AccessToken(ctx, s.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("another replica past its cache TTL: %v, want ErrNoSession", err)
	}
	if _, _, err := a.Revoke(ctx, s.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("second Revoke: %v, want ErrNoSession", err)
	}
}

func TestListAndRevokeHandleAreScopedToTheSubject(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	m := newTestManager(t, NewMemoryStore(), clk, is, nil)
	ctx := context.Background()
	first := mustCreate(t, m, is.mint(), "u1")
	clk.Advance(time.Minute)
	second := mustCreate(t, m, is.mint(), "u1")
	theirs := mustCreate(t, m, is.mint(), "u2")

	list, err := m.List(ctx, "u1")
	if err != nil || len(list) != 2 || list[0].Handle != second.Handle || list[1].Handle != first.Handle {
		t.Fatalf("List = %+v, %v; want u1's two sessions, newest first", list, err)
	}
	for _, info := range list {
		if strings.Contains(info.Handle, first.ID[len(IDPrefix):]) || info.Handle == "" {
			t.Errorf("handle %q", info.Handle)
		}
	}
	if err := m.RevokeHandle(ctx, "u1", theirs.Handle); !errors.Is(err, ErrNoSession) {
		t.Errorf("revoking another subject's session: %v, want ErrNoSession", err)
	}
	if _, err := m.AccessToken(ctx, theirs.ID); err != nil {
		t.Errorf("their session after a refused revoke: %v", err)
	}
	if err := m.RevokeHandle(ctx, "u1", "not-hex"); !errors.Is(err, ErrNoSession) {
		t.Errorf("malformed handle: %v", err)
	}
	if err := m.RevokeHandle(ctx, "u1", first.Handle); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AccessToken(ctx, first.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("revoked by handle but still accepted: %v", err)
	}
	if n, err := m.RevokeSubject(ctx, "u1"); err != nil || n != 1 {
		t.Errorf("RevokeSubject = %d, %v; want 1", n, err)
	}
	if _, err := m.AccessToken(ctx, second.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("signed out everywhere but still accepted: %v", err)
	}
	if _, err := m.AccessToken(ctx, theirs.ID); err != nil {
		t.Errorf("another subject's session after RevokeSubject: %v", err)
	}
}

func TestRevokeOthersKeepsTheNamedSessionAndOnlyThatSubjects(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	m := newTestManager(t, NewMemoryStore(), clk, is, nil)
	ctx := context.Background()
	current := mustCreate(t, m, is.mint(), "u1")
	other := mustCreate(t, m, is.mint(), "u1")
	another := mustCreate(t, m, is.mint(), "u1")
	theirs := mustCreate(t, m, is.mint(), "u2")
	// Warm the cache, so the revocation must evict, not merely delete.
	for _, s := range []Issued{current, other, another, theirs} {
		if _, err := m.AccessToken(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := m.RevokeOthers(ctx, "u1", theirs.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("naming another subject's session: %v, want ErrNoSession", err)
	}
	if _, err := m.RevokeOthers(ctx, "u1", "fs_not-an-id"); !errors.Is(err, ErrNoSession) {
		t.Errorf("malformed id: %v, want ErrNoSession", err)
	}
	if list, _ := m.List(ctx, "u1"); len(list) != 3 {
		t.Fatalf("a refused RevokeOthers ended sessions: %d left", len(list))
	}
	n, err := m.RevokeOthers(ctx, "u1", current.ID)
	if err != nil || n != 2 {
		t.Fatalf("RevokeOthers = %d, %v; want 2", n, err)
	}
	for _, s := range []Issued{other, another} {
		if _, err := m.AccessToken(ctx, s.ID); !errors.Is(err, ErrNoSession) {
			t.Errorf("another session of u1 still accepted: %v", err)
		}
	}
	if _, err := m.AccessToken(ctx, current.ID); err != nil {
		t.Errorf("the kept session: %v", err)
	}
	if _, err := m.AccessToken(ctx, theirs.ID); err != nil {
		t.Errorf("u2's session: %v", err)
	}
}

func TestSweepAndRun(t *testing.T) {
	clk := newClock()
	store := NewMemoryStore()
	m := newTestManager(t, store, clk, nil, func(c *Config) { c.SweepInterval = time.Millisecond })
	mustCreate(t, m, Tokens{AccessToken: "a", Expiry: clk.Now().Add(time.Hour)}, "u1")
	clk.Advance(DefaultLifetime)
	mustCreate(t, m, Tokens{AccessToken: "b", Expiry: clk.Now().Add(time.Hour)}, "u1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		n := len(store.records)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run did not sweep the ended session (%d rows)", n)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v, want nil on cancel", err)
	}
}

func TestMalformedIDsAndConfig(t *testing.T) {
	clk := newClock()
	m := newTestManager(t, NewMemoryStore(), clk, nil, nil)
	ctx := context.Background()
	for _, id := range []string{"", "eyJ.e30.x", "fs_short"} {
		if _, err := m.AccessToken(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Errorf("AccessToken(%q) = %v", id, err)
		}
	}
	unknown, _ := NewID()
	if _, err := m.AccessToken(ctx, unknown); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown id: %v", err)
	}
	if _, err := m.Create(ctx, Tokens{AccessToken: "a"}, Meta{}); err == nil {
		t.Error("Create with no subject succeeded")
	}
	if _, err := m.Create(ctx, Tokens{}, Meta{Subject: "u"}); err == nil {
		t.Error("Create with no access token succeeded")
	}
	if _, err := NewManager(Config{}); err == nil {
		t.Error("NewManager with no store succeeded")
	}
}

func TestInfoAndMarkProved(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	m := newTestManager(t, NewMemoryStore(), clk, is, nil)
	ctx := context.Background()
	signedIn := clk.Now()
	s, err := m.Create(ctx, is.mint(), Meta{Subject: "u1", IssuerSession: "idp-session-1"})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Hour) // a renewal must not count as a proof
	if _, err := m.AccessToken(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	info, err := m.Info(ctx, s.ID)
	if err != nil || !info.ProvedAt.Equal(signedIn) || info.Subject != "u1" || info.Handle != s.Handle {
		t.Fatalf("Info = %+v, %v; want ProvedAt at sign-in", info, err)
	}
	if err := m.MarkProved(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if info, _ := m.Info(ctx, s.ID); !info.ProvedAt.Equal(clk.Now()) {
		t.Errorf("after MarkProved ProvedAt = %v, want %v", info.ProvedAt, clk.Now())
	}
	unknown, _ := NewID()
	if err := m.MarkProved(ctx, unknown); !errors.Is(err, ErrNoSession) {
		t.Errorf("MarkProved(unknown) = %v", err)
	}
	if _, err := m.Info(ctx, "garbage"); !errors.Is(err, ErrNoSession) {
		t.Errorf("Info(garbage) = %v", err)
	}
}

// pausingStore pauses one store call at a chosen point, so a test can put a
// revocation exactly between a resolution's store read and its cache
// publication.
type pausingStore struct {
	Store
	// afterGet / afterUpdate / beforeDelete, when set, run once at that
	// point of the next such call, then are cleared.
	mu                                  sync.Mutex
	afterGet, afterUpdate, beforeDelete func()
}

func (p *pausingStore) take(f *func()) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	g := *f
	*f = nil
	return g
}

func (p *pausingStore) Get(ctx context.Context, key []byte) (Record, error) {
	r, err := p.Store.Get(ctx, key)
	if g := p.take(&p.afterGet); g != nil {
		g()
	}
	return r, err
}

func (p *pausingStore) Update(ctx context.Context, key []byte, fn func(*Record) error) error {
	err := p.Store.Update(ctx, key, fn)
	if g := p.take(&p.afterUpdate); g != nil {
		g()
	}
	return err
}

func (p *pausingStore) Delete(ctx context.Context, key []byte) error {
	if g := p.take(&p.beforeDelete); g != nil {
		g()
	}
	return p.Store.Delete(ctx, key)
}

func (p *pausingStore) Take(ctx context.Context, key []byte) (Record, error) {
	if g := p.take(&p.beforeDelete); g != nil {
		g()
	}
	return p.Store.Take(ctx, key)
}

// pause arms *hook to block until release is closed, and returns the
// channel closed when the paused call reaches it.
func (p *pausingStore) pause(hook *func(), release <-chan struct{}) <-chan struct{} {
	reached := make(chan struct{})
	p.mu.Lock()
	*hook = func() { close(reached); <-release }
	p.mu.Unlock()
	return reached
}

// A resolution that read the session before a revocation on THIS replica
// and publishes to the cache after it never brings the session back: the
// next request is refused. Every revocation path, and both the plain read
// and the refresh write, are covered.
func TestRevocationRacingAResolutionIsNotUndoneByTheCache(t *testing.T) {
	revocations := map[string]func(m *Manager, s Issued) error{
		"Revoke":        func(m *Manager, s Issued) error { _, _, err := m.Revoke(context.Background(), s.ID); return err },
		"RevokeHandle":  func(m *Manager, s Issued) error { return m.RevokeHandle(context.Background(), "u1", s.Handle) },
		"RevokeSubject": func(m *Manager, s Issued) error { _, err := m.RevokeSubject(context.Background(), "u1"); return err },
	}
	for name, revoke := range revocations {
		for _, path := range []string{"read", "refresh"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				clk := newClock()
				is := newIssuer(clk)
				store := &pausingStore{Store: NewMemoryStore()}
				m := newTestManager(t, store, clk, is, nil)
				s := mustCreate(t, m, is.mint(), "u1")
				hook := &store.afterGet
				if path == "refresh" {
					clk.Advance(is.life - time.Minute) // due: AccessToken refreshes
					hook = &store.afterUpdate
				} else {
					clk.Advance(DefaultCacheTTL) // Create's cache entry is gone
				}
				release := make(chan struct{})
				reached := store.pause(hook, release)
				done := make(chan error, 1)
				go func() { _, err := m.AccessToken(context.Background(), s.ID); done <- err }()
				<-reached
				if err := revoke(m, s); err != nil {
					t.Fatalf("revoke: %v", err)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatalf("the resolution that read before the revocation: %v", err)
				}
				if _, err := m.AccessToken(context.Background(), s.ID); !errors.Is(err, ErrNoSession) {
					t.Fatalf("after the revocation: %v, want ErrNoSession (a racing resolution cached the revoked session)", err)
				}
			})
		}
	}
}

// A resolution that reads the session after a revocation began but before
// its delete landed, and publishes in that window, is evicted when the
// revocation finishes.
func TestAResolutionInsideARevocationIsEvictedWhenItEnds(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	store := &pausingStore{Store: NewMemoryStore()}
	m := newTestManager(t, store, clk, is, nil)
	s := mustCreate(t, m, is.mint(), "u1")
	clk.Advance(DefaultCacheTTL)
	release := make(chan struct{})
	reached := store.pause(&store.beforeDelete, release)
	done := make(chan error, 1)
	go func() { _, _, err := m.Revoke(context.Background(), s.ID); done <- err }()
	<-reached
	if _, err := m.AccessToken(context.Background(), s.ID); err != nil {
		t.Fatalf("before the delete landed the row is still there: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := m.AccessToken(context.Background(), s.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after the revocation: %v, want ErrNoSession", err)
	}
}

// A session in use keeps extending its idle deadline even when its token
// cannot be renewed: a token shorter-lived than RefreshBefore with no
// refresh token, or an issuer that is down while the token still works.
func TestUseExtendsTheIdleDeadlineWithoutARefresh(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(is *issuer) (Refresher, Tokens)
	}{
		{"no refresh token", func(is *issuer) (Refresher, Tokens) {
			tok := is.mint()
			tok.RefreshToken = ""
			return nil, tok
		}},
		{"issuer down", func(is *issuer) (Refresher, Tokens) {
			is.setFail(errors.New("issuer unreachable"))
			return is, is.mint()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			is := newIssuer(clk)
			is.life = time.Minute // below DefaultRefreshBefore: never "fresh"
			r, tok := tc.setup(is)
			m := newTestManager(t, NewMemoryStore(), clk, r, func(c *Config) {
				c.IdleTimeout, c.CacheTTL = 30*time.Second, -1
			})
			s := mustCreate(t, m, tok, "u1")
			for i := 1; i <= 5; i++ { // a request every 10s, 50s in all
				clk.Advance(10 * time.Second)
				if _, err := m.AccessToken(context.Background(), s.ID); err != nil {
					t.Fatalf("request at +%ds of a session in use (idle timeout 30s): %v", 10*i, err)
				}
			}
		})
	}
}

// A session used only through the cache still has its idle deadline pushed
// out: with an idle timeout shorter than the cache TTL, a request every 2s
// keeps it alive however many of them the cache answers.
func TestCachedUseStillExtendsTheIdleDeadline(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	m := newTestManager(t, NewMemoryStore(), clk, is, func(c *Config) {
		c.IdleTimeout, c.CacheTTL = 5*time.Second, 15*time.Second
	})
	s := mustCreate(t, m, is.mint(), "u1")
	for i := 1; i <= 15; i++ { // 30s of use, six idle timeouts
		clk.Advance(2 * time.Second)
		if _, err := m.AccessToken(context.Background(), s.ID); err != nil {
			t.Fatalf("request at +%ds of a session in use (idle timeout 5s, cache TTL 15s): %v", 2*i, err)
		}
	}
	clk.Advance(6 * time.Second)
	if _, err := m.AccessToken(context.Background(), s.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after 6s unused (idle timeout 5s): %v, want ErrNoSession", err)
	}
}

// gatedRefresher blocks each Refresh until release is closed, and closes
// entered when the first one starts.
type gatedRefresher struct {
	Refresher
	entered, release chan struct{}
	once             sync.Once
}

func (g *gatedRefresher) Refresh(ctx context.Context, rt string) (Tokens, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.Refresher.Refresh(ctx, rt)
}

// A sign-out racing a renewal returns the refresh token the renewal rotated
// in, so the caller revokes the live one at the issuer, not the one the
// renewal already spent.
func TestRevokeRacingARefreshReturnsTheRotatedTokens(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	g := &gatedRefresher{Refresher: is, entered: make(chan struct{}), release: make(chan struct{})}
	store := &pausingStore{Store: NewMemoryStore()}
	m := newTestManager(t, store, clk, g, nil)
	s := mustCreate(t, m, is.mint(), "u1") // rt-1
	clk.Advance(is.life - time.Minute)     // due: AccessToken refreshes

	refreshed := make(chan error, 1)
	go func() { _, err := m.AccessToken(context.Background(), s.ID); refreshed <- err }()
	<-g.entered // the renewal holds the session and is at the issuer

	closed := make(chan struct{})
	close(closed)
	taking := store.pause(&store.beforeDelete, closed)
	type result struct {
		t   Tokens
		err error
	}
	revoked := make(chan result, 1)
	go func() { tok, _, err := m.Revoke(context.Background(), s.ID); revoked <- result{tok, err} }()
	<-taking // Revoke is removing the session
	close(g.release)

	if err := <-refreshed; err != nil {
		t.Fatalf("the renewal: %v", err)
	}
	r := <-revoked
	if r.err != nil {
		t.Fatalf("Revoke: %v", r.err)
	}
	if r.t.RefreshToken != "rt-2" {
		t.Fatalf("Revoke returned refresh token %q, want rt-2: the one the racing renewal rotated in (rt-1 is already spent)", r.t.RefreshToken)
	}
	if _, err := m.AccessToken(context.Background(), s.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after Revoke: %v, want ErrNoSession", err)
	}
}
