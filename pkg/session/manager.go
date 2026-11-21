package session

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Refresher redeems a refresh token at the issuer (RFC 6749 §6) for a new set
// of tokens. A Refresher that cannot redeem the token because the issuer
// REFUSED it — revoked, expired, already used — returns an error wrapping
// [ErrRefreshRejected]; the session then ends. Any other error is treated as
// the issuer being unreachable, and the session survives it.
//
// A returned Tokens may leave RefreshToken or IDToken empty: the previous
// values are kept. Issuers that rotate refresh tokens return a new one, and
// the Manager stores it in the same write that stores the access token.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (Tokens, error)
}

// RefresherFunc adapts a function to a [Refresher].
type RefresherFunc func(ctx context.Context, refreshToken string) (Tokens, error)

// Refresh calls f.
func (f RefresherFunc) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	return f(ctx, refreshToken)
}

// ErrRefreshRejected is the error a [Refresher] wraps when the issuer refused
// the refresh token (OAuth's invalid_grant). The session it belonged to ends.
var ErrRefreshRejected = errors.New("session: the issuer rejected the refresh token")

// Defaults for the [Config] fields left zero.
const (
	DefaultLifetime       = 30 * 24 * time.Hour
	DefaultIdleTimeout    = 14 * 24 * time.Hour
	DefaultRefreshBefore  = 2 * time.Minute
	DefaultCacheTTL       = 15 * time.Second
	DefaultRefreshTimeout = 10 * time.Second
	DefaultSweepInterval  = time.Hour

	// unknownExpiry is how long an access token whose expiry nobody stated
	// is trusted before a refresh is attempted.
	unknownExpiry = 5 * time.Minute
	// touchEvery is how stale last_seen_at may get before a use writes it:
	// the idle timeout is measured in days, so a write per request would
	// buy nothing but load.
	touchEvery = time.Minute
	// cacheMax bounds the in-process cache; past it the cache starts over.
	cacheMax = 10000
	// userAgentMax bounds the stored User-Agent.
	userAgentMax = 512
)

// Config configures a [Manager]. Only Store is required.
type Config struct {
	// Store holds the sessions. Required.
	Store Store
	// Refresher renews access tokens. nil means sessions end when their
	// access token expires, which is what a cookie holding the token
	// itself did.
	Refresher Refresher

	// Lifetime is a session's absolute length, however actively it is
	// used. 0 is DefaultLifetime (30 days).
	Lifetime time.Duration
	// IdleTimeout ends a session that is not used for this long. 0 is
	// DefaultIdleTimeout (14 days); negative disables it, so only Lifetime
	// ends a session.
	IdleTimeout time.Duration
	// RefreshBefore is how long before the access token expires it is
	// renewed. 0 is DefaultRefreshBefore (2 minutes).
	RefreshBefore time.Duration
	// CacheTTL is how long a replica reuses an access token it read
	// without asking the store again. It bounds how long a session
	// revoked on ANOTHER replica keeps working here; a revocation on the
	// same replica takes effect at once. 0 is DefaultCacheTTL (15
	// seconds); negative disables the cache.
	CacheTTL time.Duration
	// RefreshTimeout bounds one call to the Refresher. 0 is
	// DefaultRefreshTimeout (10 seconds).
	RefreshTimeout time.Duration
	// SweepInterval is how often Run deletes ended sessions. 0 is
	// DefaultSweepInterval (an hour); negative disables sweeping.
	SweepInterval time.Duration

	// Logger receives refresh failures and sweeps. nil is slog.Default().
	Logger *slog.Logger
	// Now is the clock. nil is time.Now.
	Now func() time.Time
}

// Manager creates, resolves, refreshes and revokes sessions over a [Store].
// It is safe for concurrent use; one Manager per process serves every
// request.
type Manager struct {
	cfg Config

	mu      sync.Mutex
	cache   map[string]cacheEntry
	flights map[string]*flight
	// revocations counts the revocations this process has begun or
	// finished (see revoking). A cache entry is published only when it has
	// not moved since the store read the entry is built from, so a read
	// that raced a revocation never brings the revoked session back.
	revocations uint64

	noRefreshOnce sync.Once
}

type cacheEntry struct {
	token   string
	subject string
	until   time.Time
}

type flight struct {
	done  chan struct{}
	token string
	err   error
}

// NewManager validates cfg, fills its defaults and returns a Manager.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, errors.New("session: a Store is required")
	}
	if cfg.Lifetime == 0 {
		cfg.Lifetime = DefaultLifetime
	}
	if cfg.Lifetime < 0 {
		return nil, fmt.Errorf("session: Lifetime %s is negative", cfg.Lifetime)
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.RefreshBefore <= 0 {
		cfg.RefreshBefore = DefaultRefreshBefore
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.RefreshTimeout <= 0 {
		cfg.RefreshTimeout = DefaultRefreshTimeout
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = DefaultSweepInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg, cache: map[string]cacheEntry{}, flights: map[string]*flight{}}, nil
}

// Meta describes a new session.
type Meta struct {
	// Subject is the identity provider's subject the session belongs to.
	// Required.
	Subject string
	// UserAgent is the request's User-Agent, kept for an account page's
	// list of sessions. Optional.
	UserAgent string
	// IssuerSession is the identity provider's own session reference, when
	// the sign-in has one. Optional. It is stored in the clear: a reference
	// only, never the issuer session's token (that travels sealed, in
	// Tokens.IssuerSessionToken).
	IssuerSession string
	// ProvedAt is when the person last proved a credential for this
	// session. Zero (the usual case) is now: the session is being created
	// by a sign-in. A session created from ANOTHER session's approval (a
	// handoff) passes that session's ProvedAt instead, so an action that
	// demands a recent proof is not satisfied by a click. A time after now
	// is read as now.
	ProvedAt time.Time
	// Channel names how the session was created when it was not by a
	// sign-in of its own: [ChannelHandoff] for one a signed-in session
	// handed to another client. "" is a sign-in. It is sealed with the
	// tokens, so [Manager.Info] reports it and [Manager.List] cannot.
	Channel string
}

// ChannelHandoff is the [Meta.Channel] of a session created by a sign-in
// handoff: another client's session approved it, so no credential was proved
// for it here.
const ChannelHandoff = "handoff"

// Issued is a session [Manager.Create] made.
type Issued struct {
	// ID is the session id: the cookie's value and the session's only
	// secret. Never log it.
	ID string
	// Handle names the session without being a credential (see Info).
	Handle string
	// ExpiresAt is the session's absolute end; a cookie carrying ID may
	// expire then.
	ExpiresAt time.Time
}

// Info describes a session for an account page: when and from what it was
// created, when it was last used, when it ends. It holds no credential.
type Info struct {
	// Handle names the session for [Manager.RevokeHandle]. It is derived
	// from the id, and the id cannot be recovered from it.
	Handle    string
	Subject   string
	UserAgent string
	CreatedAt time.Time
	// ProvedAt is when a credential was last proved for the session: its
	// sign-in, or a later MarkProved. An action that wants a recent proof
	// (changing an address, revoking another session) compares against it.
	ProvedAt   time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	// Channel is the session's Meta.Channel ("" for a sign-in). Only
	// [Manager.Info] and [Manager.Open], which hold the id, can read it.
	Channel string
}

func infoOf(r Record) Info {
	return Info{Handle: handleOf(r.Key), Subject: r.Subject, UserAgent: r.UserAgent,
		CreatedAt: r.CreatedAt, ProvedAt: r.ProvedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt}
}

// Create stores tokens as a new session of meta.Subject and returns its id.
//
// A zero tokens.Expiry is read as "due for a refresh in five minutes". A
// session with no refresh token ends when its access token expires; ask the
// issuer for one (OIDC's offline_access scope).
func (m *Manager) Create(ctx context.Context, tokens Tokens, meta Meta) (Issued, error) {
	if meta.Subject == "" {
		return Issued{}, errors.New("session: a session needs a subject")
	}
	if tokens.AccessToken == "" {
		return Issued{}, errors.New("session: a session needs an access token")
	}
	id, err := NewID()
	if err != nil {
		return Issued{}, err
	}
	k, err := deriveKeys(id)
	if err != nil {
		return Issued{}, err
	}
	now := m.cfg.Now()
	if tokens.Expiry.IsZero() {
		tokens.Expiry = now.Add(unknownExpiry)
	}
	sealed, err := sealWith(k, tokens, meta.Channel)
	if err != nil {
		return Issued{}, err
	}
	proved := now
	if !meta.ProvedAt.IsZero() && meta.ProvedAt.Before(now) {
		proved = meta.ProvedAt
	}
	ua := meta.UserAgent
	if len(ua) > userAgentMax {
		ua = ua[:userAgentMax]
	}
	expires := now.Add(m.cfg.Lifetime)
	since := m.epoch()
	rec := Record{Key: k.lookup, Subject: meta.Subject, Sealed: sealed, UserAgent: ua, IssuerSession: meta.IssuerSession,
		CreatedAt: now, ProvedAt: proved, LastSeenAt: now, RefreshedAt: now,
		IdleExpiresAt: m.idleFrom(now, expires), ExpiresAt: expires}
	if err := m.cfg.Store.Create(ctx, rec); err != nil {
		return Issued{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if tokens.RefreshToken == "" {
		m.noRefreshOnce.Do(func() {
			m.cfg.Logger.WarnContext(ctx, "the issuer returned no refresh token, so sessions end when their access token expires; "+
				"request the offline_access scope (oidc_scopes) to keep people signed in")
		})
	}
	m.remember(k.lookup, tokens, rec, now, since)
	return Issued{ID: id, Handle: handleOf(k.lookup), ExpiresAt: expires}, nil
}

// idleFrom is the idle deadline of a session used at now that ends at
// expires.
func (m *Manager) idleFrom(now, expires time.Time) time.Time {
	if m.cfg.IdleTimeout < 0 {
		return expires
	}
	if idle := now.Add(m.cfg.IdleTimeout); idle.Before(expires) {
		return idle
	}
	return expires
}

// AccessToken returns the access token session id stands for, renewing it
// first when it is about to expire. It fails with [ErrNoSession] when the
// session does not exist, has ended or was revoked — the caller is signed
// out — and with an error wrapping [ErrUnavailable] when the store or issuer
// could not be reached and no still-valid token is at hand.
//
// A renewal happens once per session however many requests ask at once:
// requests in this process wait on one call, and the store's row lock
// serializes replicas, which then find the renewed token rather than
// spending a rotated refresh token twice.
func (m *Manager) AccessToken(ctx context.Context, id string) (string, error) {
	k, err := deriveKeys(id)
	if err != nil {
		return "", ErrNoSession
	}
	now := m.cfg.Now()
	if token, ok := m.cached(k.lookup, now); ok {
		return token, nil
	}
	since := m.epoch()
	rec, err := m.cfg.Store.Get(ctx, k.lookup)
	if errors.Is(err, ErrNoSession) {
		return "", ErrNoSession
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if rec.expired(now) {
		m.drop(ctx, k.lookup)
		return "", ErrNoSession
	}
	t, err := open(k, rec.Sealed)
	if err != nil {
		m.drop(ctx, k.lookup)
		return "", ErrNoSession
	}
	if m.fresh(t, now) {
		rec = m.touch(ctx, k.lookup, rec, now)
		m.remember(k.lookup, t, rec, now, since)
		return t.AccessToken, nil
	}
	return m.refresh(ctx, k)
}

// fresh reports whether t's access token is good for longer than
// RefreshBefore.
func (m *Manager) fresh(t Tokens, now time.Time) bool {
	return !t.Expiry.IsZero() && now.Add(m.cfg.RefreshBefore).Before(t.Expiry)
}

// valid reports whether t's access token has not expired yet.
func valid(t Tokens, now time.Time) bool {
	return t.AccessToken != "" && !t.Expiry.IsZero() && now.Before(t.Expiry)
}

// touchInterval is how stale a session's LastSeenAt may get before a use
// writes it: touchEvery, or half the IdleTimeout when that is shorter (so a
// short idle timeout is still extended before it ends a session in use).
func (m *Manager) touchInterval() time.Duration {
	if half := m.cfg.IdleTimeout / 2; half < touchEvery {
		return half
	}
	return touchEvery
}

// touch pushes the idle deadline out, at most once per touchInterval, and
// returns rec as the store now holds it. A failed write costs nothing but an
// earlier idle end, so it is only logged, and rec comes back unchanged.
func (m *Manager) touch(ctx context.Context, key []byte, rec Record, now time.Time) Record {
	if m.cfg.IdleTimeout < 0 || rec.Key == nil || now.Sub(rec.LastSeenAt) < m.touchInterval() {
		return rec
	}
	err := m.cfg.Store.Update(ctx, key, func(r *Record) error {
		r.LastSeenAt = now
		r.IdleExpiresAt = m.idleFrom(now, r.ExpiresAt)
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrNoSession) {
			m.cfg.Logger.DebugContext(ctx, "session: could not record a use", "error", err)
		}
		return rec
	}
	rec.LastSeenAt, rec.IdleExpiresAt = now, m.idleFrom(now, rec.ExpiresAt)
	return rec
}

// Sentinels the refresh callback returns to abort the store's write.
var (
	errFresh         = errors.New("session: already refreshed")
	errCannotRefresh = errors.New("session: no way to refresh")
)

// refresh renews session k's tokens, once per session in this process.
func (m *Manager) refresh(ctx context.Context, k keys) (string, error) {
	key := string(k.lookup)
	m.mu.Lock()
	if f, ok := m.flights[key]; ok {
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.token, f.err
		case <-ctx.Done():
			return "", fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
		}
	}
	f := &flight{done: make(chan struct{})}
	m.flights[key] = f
	m.mu.Unlock()

	// The renewal belongs to every request waiting on it, so one caller
	// giving up must not cancel it for the others.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.RefreshTimeout)
	f.token, f.err = m.doRefresh(rctx, k)
	cancel()

	m.mu.Lock()
	delete(m.flights, key)
	m.mu.Unlock()
	close(f.done)
	return f.token, f.err
}

func (m *Manager) doRefresh(ctx context.Context, k keys) (string, error) {
	var (
		got, latest Tokens
		after, read Record
	)
	since := m.epoch()
	err := m.cfg.Store.Update(ctx, k.lookup, func(r *Record) error {
		now := m.cfg.Now()
		if r.expired(now) {
			return ErrNoSession
		}
		t, channel, err := openWith(k, r.Sealed)
		if err != nil {
			return err
		}
		latest, read = t, *r
		if m.fresh(t, now) {
			// Another replica renewed it while this one waited for the row.
			got, after = t, *r
			return errFresh
		}
		if m.cfg.Refresher == nil || t.RefreshToken == "" {
			return errCannotRefresh
		}
		next, err := m.cfg.Refresher.Refresh(ctx, t.RefreshToken)
		if err != nil {
			return err
		}
		if next.AccessToken == "" {
			return errors.New("session: the issuer returned no access token")
		}
		if next.RefreshToken == "" {
			next.RefreshToken = t.RefreshToken
		}
		if next.IDToken == "" {
			next.IDToken = t.IDToken
		}
		if next.Expiry.IsZero() {
			next.Expiry = now.Add(unknownExpiry)
		}
		if next.IssuerSessionID == "" {
			// A refresh does not touch the issuer's session: keep it.
			next.IssuerSessionID, next.IssuerSessionToken = t.IssuerSessionID, t.IssuerSessionToken
		}
		sealed, err := sealWith(k, next, channel)
		if err != nil {
			return err
		}
		r.Sealed, r.RefreshedAt, r.LastSeenAt = sealed, now, now
		r.IdleExpiresAt = m.idleFrom(now, r.ExpiresAt)
		got, after = next, *r
		return nil
	})
	now := m.cfg.Now()
	switch {
	case err == nil || errors.Is(err, errFresh):
		m.remember(k.lookup, got, after, now, since)
		return got.AccessToken, nil
	case errors.Is(err, ErrNoSession), errors.Is(err, errCorrupt), errors.Is(err, ErrRefreshRejected):
		if errors.Is(err, ErrRefreshRejected) {
			m.cfg.Logger.InfoContext(ctx, "session: the issuer refused the refresh token; the session ended", "error", err)
		}
		m.drop(ctx, k.lookup)
		return "", ErrNoSession
	case errors.Is(err, errCannotRefresh):
		if valid(latest, now) {
			// A use like any other: the session's idle deadline moves
			// even though its token could not be renewed.
			m.touch(ctx, k.lookup, read, now)
			return latest.AccessToken, nil
		}
		m.drop(ctx, k.lookup)
		return "", ErrNoSession
	default:
		// The issuer or the store is unreachable: the session is not
		// ended over it. While its token is still valid it keeps working.
		if valid(latest, now) {
			m.cfg.Logger.WarnContext(ctx, "session: refresh failed; using the current access token until it expires", "error", err)
			m.touch(ctx, k.lookup, read, now)
			return latest.AccessToken, nil
		}
		return "", fmt.Errorf("%w: refresh: %w", ErrUnavailable, err)
	}
}

// Info describes session id without renewing it, or ErrNoSession.
func (m *Manager) Info(ctx context.Context, id string) (Info, error) {
	k, err := deriveKeys(id)
	if err != nil {
		return Info{}, ErrNoSession
	}
	rec, err := m.cfg.Store.Get(ctx, k.lookup)
	if errors.Is(err, ErrNoSession) {
		return Info{}, ErrNoSession
	}
	if err != nil {
		return Info{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if rec.expired(m.cfg.Now()) {
		return Info{}, ErrNoSession
	}
	info := infoOf(rec)
	if _, channel, err := openWith(k, rec.Sealed); err == nil {
		info.Channel = channel
	}
	return info, nil
}

// Open returns the tokens session id holds, as stored — without renewing
// them, so the access token may be expired — and its Info, or ErrNoSession.
// It is for a server-side step that needs more of the session than its access
// token: a sign-in handoff reads the issuer session it was created from.
// Never send what it returns to a client.
func (m *Manager) Open(ctx context.Context, id string) (Tokens, Info, error) {
	k, err := deriveKeys(id)
	if err != nil {
		return Tokens{}, Info{}, ErrNoSession
	}
	rec, err := m.cfg.Store.Get(ctx, k.lookup)
	if errors.Is(err, ErrNoSession) {
		return Tokens{}, Info{}, ErrNoSession
	}
	if err != nil {
		return Tokens{}, Info{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if rec.expired(m.cfg.Now()) {
		return Tokens{}, Info{}, ErrNoSession
	}
	t, channel, err := openWith(k, rec.Sealed)
	if err != nil {
		return Tokens{}, Info{}, ErrNoSession
	}
	info := infoOf(rec)
	info.Channel = channel
	return t, info, nil
}

// InfoByHandle describes the live session handle names (Info.Handle), or
// ErrNoSession. A handle is not a credential, so this reveals no token: it is
// how a step that holds only a handle — a pending handoff naming the session
// that approved it — checks that session was not revoked since. Channel is
// left empty (it is sealed under the id).
func (m *Manager) InfoByHandle(ctx context.Context, handle string) (Info, error) {
	key, err := hex.DecodeString(handle)
	if err != nil || len(key) != 32 {
		return Info{}, ErrNoSession
	}
	rec, err := m.cfg.Store.Get(ctx, key)
	if errors.Is(err, ErrNoSession) {
		return Info{}, ErrNoSession
	}
	if err != nil {
		return Info{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if rec.expired(m.cfg.Now()) {
		return Info{}, ErrNoSession
	}
	return infoOf(rec), nil
}

// Store is the [Store] the Manager was built over, for a caller that keeps
// related state beside the sessions: a sign-in handoff's one-time codes live
// in the same store when it also implements [HandoffStore].
func (m *Manager) Store() Store { return m.cfg.Store }

// MarkProved records that the person behind session id just proved a
// credential again (re-entered a password, used a passkey): ProvedAt becomes
// now. The caller performs the proof; this only records it.
func (m *Manager) MarkProved(ctx context.Context, id string) error {
	k, err := deriveKeys(id)
	if err != nil {
		return ErrNoSession
	}
	err = m.cfg.Store.Update(ctx, k.lookup, func(r *Record) error {
		now := m.cfg.Now()
		if r.expired(now) {
			return ErrNoSession
		}
		r.ProvedAt, r.LastSeenAt = now, now
		r.IdleExpiresAt = m.idleFrom(now, r.ExpiresAt)
		return nil
	})
	if err != nil && !errors.Is(err, ErrNoSession) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

// Revoke ends session id and returns the tokens it held, so the caller can
// also revoke them at the issuer (RFC 7009). The tokens are the ones the
// session held when it was removed: the store's Take waits for a renewal in
// progress on any replica, so a sign-out racing a refresh returns the
// refresh token that refresh rotated in, never the one it spent. A session
// that does not exist is ErrNoSession.
func (m *Manager) Revoke(ctx context.Context, id string) (Tokens, Info, error) {
	k, err := deriveKeys(id)
	if err != nil {
		return Tokens{}, Info{}, ErrNoSession
	}
	done := m.revoking(func(c map[string]cacheEntry) { delete(c, string(k.lookup)) })
	defer done()
	rec, err := m.cfg.Store.Take(ctx, k.lookup)
	if errors.Is(err, ErrNoSession) {
		return Tokens{}, Info{}, ErrNoSession
	}
	if err != nil {
		return Tokens{}, Info{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	t, _ := open(k, rec.Sealed)
	return t, infoOf(rec), nil
}

// List returns subject's live sessions, newest first.
func (m *Manager) List(ctx context.Context, subject string) ([]Info, error) {
	recs, err := m.cfg.Store.ListSubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	now := m.cfg.Now()
	out := make([]Info, 0, len(recs))
	for _, r := range recs {
		if !r.expired(now) {
			out = append(out, infoOf(r))
		}
	}
	return out, nil
}

// RevokeHandle ends the session handle names, if it is subject's: an account
// page signing out one of the caller's own sessions. Another subject's
// handle is ErrNoSession, exactly like an unknown one.
func (m *Manager) RevokeHandle(ctx context.Context, subject, handle string) error {
	key, err := hex.DecodeString(handle)
	if err != nil || len(key) != 32 {
		return ErrNoSession
	}
	rec, err := m.cfg.Store.Get(ctx, key)
	if errors.Is(err, ErrNoSession) {
		return ErrNoSession
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if rec.Subject != subject {
		return ErrNoSession
	}
	done := m.revoking(func(c map[string]cacheEntry) { delete(c, string(key)) })
	defer done()
	if err := m.cfg.Store.Delete(ctx, key); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// RevokeSubject ends every session of subject — "sign out everywhere", or an
// account an operator disabled — and returns how many. The tokens are not
// revoked at the issuer: they are sealed under ids nobody holds once the rows
// are gone.
func (m *Manager) RevokeSubject(ctx context.Context, subject string) (int64, error) {
	done := m.revoking(func(c map[string]cacheEntry) {
		for k, e := range c {
			if e.subject == subject {
				delete(c, k)
			}
		}
	})
	defer done()
	n, err := m.cfg.Store.DeleteSubject(ctx, subject)
	if err != nil {
		return n, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return n, nil
}

// Sweep deletes every session that has ended and returns how many. Ended
// sessions are refused whether or not they are swept; sweeping only keeps
// the table small.
func (m *Manager) Sweep(ctx context.Context) (int64, error) {
	return m.cfg.Store.DeleteExpired(ctx, m.cfg.Now())
}

// Run sweeps every SweepInterval until ctx is done, then returns nil. It is
// the lifecycle method a supervisor runs; a failed sweep is logged and
// retried at the next tick.
func (m *Manager) Run(ctx context.Context) error {
	if m.cfg.SweepInterval < 0 {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(m.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			n, err := m.Sweep(ctx)
			if err != nil {
				m.cfg.Logger.WarnContext(ctx, "session: sweep failed", "error", err)
				continue
			}
			if n > 0 {
				m.cfg.Logger.DebugContext(ctx, "session: swept ended sessions", "count", n)
			}
		}
	}
}

// ── the in-process cache ───────────────────────────────────────────────

func (m *Manager) cached(key []byte, now time.Time) (string, bool) {
	if m.cfg.CacheTTL < 0 {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[string(key)]
	if !ok || !now.Before(e.until) {
		return "", false
	}
	return e.token, true
}

// epoch is the revocation count a cache publication is checked against:
// read it BEFORE the store read the entry is built from.
func (m *Manager) epoch() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revocations
}

// revoking begins a revocation: it evicts the cache entries evict removes
// and counts the revocation, and returns the func that ends it (evicting and
// counting again) once the store's delete returned. A request that read the
// session before the delete and publishes after it finds the count moved and
// publishes nothing; one that published before the delete returned is evicted
// by the second pass. So after a revocation on this replica the next request
// reads the store, which no longer has the session.
func (m *Manager) revoking(evict func(map[string]cacheEntry)) (done func()) {
	pass := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.revocations++
		evict(m.cache)
	}
	pass()
	return pass
}

// remember caches t's access token until the earliest of: CacheTTL from now,
// the moment it is due for a refresh, the session's end, and the moment its
// next use is due to be recorded (rec.LastSeenAt plus touchInterval), so a
// session used only through the cache still has its idle deadline pushed
// out before it ends; rec is the record as stored after any touch. since is
// the epoch read before the store read rec came from; a revocation since
// then means rec may describe a session that no longer exists, so nothing is
// cached.
func (m *Manager) remember(key []byte, t Tokens, rec Record, now time.Time, since uint64) {
	if m.cfg.CacheTTL < 0 || t.AccessToken == "" {
		return
	}
	until := now.Add(m.cfg.CacheTTL)
	limits := []time.Time{t.Expiry.Add(-m.cfg.RefreshBefore), rec.IdleExpiresAt, rec.ExpiresAt}
	if m.cfg.IdleTimeout >= 0 && !rec.LastSeenAt.IsZero() {
		limits = append(limits, rec.LastSeenAt.Add(m.touchInterval()))
	}
	for _, limit := range limits {
		if !limit.IsZero() && limit.Before(until) {
			until = limit
		}
	}
	if !now.Before(until) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revocations != since {
		return
	}
	if len(m.cache) >= cacheMax {
		m.cache = map[string]cacheEntry{}
	}
	m.cache[string(key)] = cacheEntry{token: t.AccessToken, subject: rec.Subject, until: until}
}

func (m *Manager) forget(key []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, string(key))
}

// drop forgets and deletes an ended session. A failed delete is harmless:
// the session is refused anyway and the sweep removes it.
func (m *Manager) drop(ctx context.Context, key []byte) {
	m.forget(key)
	if err := m.cfg.Store.Delete(ctx, key); err != nil {
		m.cfg.Logger.DebugContext(ctx, "session: could not delete an ended session", "error", err)
	}
}
