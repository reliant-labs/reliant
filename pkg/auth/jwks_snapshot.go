package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// JWKS snapshot fallback.
//
// A validator that fetches its issuer's keys live refuses to start when the
// issuer is unreachable at boot. That is right for an issuer one app depends
// on, and a fleet-wide hazard for a shared one: every app that restarts during
// the issuer's outage crash-loops. A JWKS SNAPSHOT is the last-known public
// keys, delivered out of band (a file, or a config value from the secret
// store), that lets such an app start anyway. It is only ever a fallback for
// KEY MATERIAL: tokens are still verified (signature, alg, iss, aud, exp).
//
// ── Document format ───────────────────────────────────────────────────
//
// A JWKS document with two extra members, so it is still a valid JWKS:
//
//	{
//	  "issuer": "https://acme.example",          // optional; checked against the validator's issuer
//	  "delivered_at": "2026-10-15T03:00:00Z",    // RFC 3339; when the publisher took the snapshot
//	  "keys": [ {"kty":"RSA","kid":"…","alg":"RS256","n":"…","e":"AQAB"} ]
//	}
//
// JWKS keys carry no expiry, so age is judged from delivered_at against MaxAge
// (default 7 days). A file without delivered_at falls back to its mtime; an
// inline value without one is unusable, because nothing else says how old it is.
//
// ── Which keys are trusted, and when ──────────────────────────────────
//
//	issuer  snapshot             behaviour
//	------  -------------------  ------------------------------------------
//	up      any                  live keys only, with ONE exception below
//	down    fresh                start on the snapshot; keep retrying live;
//	                             the first live success replaces it
//	down    stale/absent/bad     REFUSE TO START (no keys at all)
//
// Live wins. For any kid the live set holds, the live key is used even when
// the snapshot holds a different key under the same kid. The exception is a
// rotation overlap: a token signed with a kid the live set does not (yet)
// hold is checked against the snapshot only when the snapshot was delivered
// AFTER this process last fetched the live set. That is the one situation
// where the snapshot is provably newer information than the live cache: the
// publisher saw a rotation this process has not refreshed for. Once the live
// set is fetched again, the snapshot stops counting for that kid; a key the
// issuer has dropped or revoked is therefore rejected no later than the next
// live refresh, never kept alive by an old snapshot.
//
// While running on a snapshot only, it stops being accepted once it ages past
// MaxAge: the validator fails closed (and keeps retrying live) rather than
// trusting week-old keys indefinitely.

// DefaultJWKSSnapshotMaxAge is how old a snapshot may be and still be trusted
// when [JWKSSnapshot.MaxAge] is unset.
const DefaultJWKSSnapshotMaxAge = 7 * 24 * time.Hour

// JWKSSnapshot configures the offline fallback key source of a JWKS-backed
// validator. The zero value means no snapshot, and the validator keeps its
// strict live-only behaviour.
type JWKSSnapshot struct {
	// File is a path to a snapshot document. It is re-read when its content
	// changes, so a re-delivery on key rotation needs no restart.
	File string

	// Inline is a snapshot document delivered as a config value (the
	// secret-store path). It is parsed once, at construction.
	Inline string

	// MaxAge bounds how old a snapshot's delivered_at may be. Zero means
	// [DefaultJWKSSnapshotMaxAge].
	MaxAge time.Duration

	// Issuer, when set, must equal the snapshot's issuer member (if the
	// snapshot names one): a snapshot published for another issuer is
	// refused, not merged. [NewValidator] fills it from JWTConfig.Issuer.
	Issuer string
}

func (s JWKSSnapshot) configured() bool { return s.File != "" || s.Inline != "" }

func (s JWKSSnapshot) maxAge() time.Duration {
	if s.MaxAge > 0 {
		return s.MaxAge
	}
	return DefaultJWKSSnapshotMaxAge
}

// Retry cadence of the background live fetch, and how long the first attempt
// may take. Variables so tests can shrink them.
var (
	jwksRetryInitial    = 5 * time.Second
	jwksRetryMax        = 5 * time.Minute
	jwksBootAttempt     = 10 * time.Second
	snapshotFileRecheck = 5 * time.Second
	// snapshotFutureSkew is how far in the future a delivered_at may be. A
	// timestamp beyond it is refused: a snapshot cannot be made perpetually
	// fresh, or "newer than the live set", by dating it ahead.
	snapshotFutureSkew = 5 * time.Minute
)

// liveJWKS is a working live key source and when it last fetched the issuer's
// keys successfully.
type liveJWKS struct {
	kf     keyfunc.Keyfunc
	lastOK *atomic.Int64 // unix nanos of the last HTTP 200 for the JWKS URL
}

// holds reports whether the live set currently cached holds kid, without the
// refresh an unknown kid would trigger on the keyfunc itself (which waits on a
// rate limiter for up to a minute).
func (l *liveJWKS) holds(ctx context.Context, kid string) bool {
	keys, err := l.kf.Storage().KeyReadAll(ctx)
	if err != nil {
		return false
	}
	for _, k := range keys {
		if k.Marshal().KID == kid {
			return true
		}
	}
	return false
}

func (l *liveJWKS) lastFetch() time.Time {
	n := l.lastOK.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// okTracker records the time of each successful JWKS response.
type okTracker struct {
	base http.RoundTripper
	last *atomic.Int64
}

func (t okTracker) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		t.last.Store(time.Now().UnixNano())
	}
	return resp, err
}

// jwksResolver yields the JWKS URL for one live attempt: a fixed URL, or OIDC
// discovery re-run on every attempt (the issuer that is down is the one
// discovery asks).
type jwksResolver func(ctx context.Context) (string, error)

func (j JWTConfig) resolver() jwksResolver {
	if j.JWKSURL != "" {
		u := j.JWKSURL
		return func(context.Context) (string, error) { return u, nil }
	}
	return jwksResolver(j.JWKSDiscover)
}

// attemptLive makes one fail-fast attempt to reach the issuer's keys. The
// returned cancel stops the refresh goroutine the attempt started.
func attemptLive(ctx context.Context, resolve jwksResolver, fetchTimeout time.Duration) (*liveJWKS, context.CancelFunc, error) {
	actx, cancel := context.WithCancel(ctx)
	rctx := actx
	if fetchTimeout > 0 {
		var rcancel context.CancelFunc
		rctx, rcancel = context.WithTimeout(actx, fetchTimeout)
		defer rcancel()
	}
	url, err := resolve(rctx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	last := new(atomic.Int64)
	failFast := false
	kf, err := keyfunc.NewDefaultOverrideCtx(actx, []string{url}, keyfunc.Override{
		NoErrorReturnFirstHTTPReq: &failFast,
		HTTPTimeout:               fetchTimeout,
		Client:                    &http.Client{Transport: okTracker{base: http.DefaultTransport, last: last}},
	})
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("auth: fetch JWKS from %v: %w", url, err)
	}
	return &liveJWKS{kf: kf, lastOK: last}, cancel, nil
}

// newJWKSKeyfunc builds the key source of a JWKS-backed validator and returns
// the function that stops its background work.
//
// Without a snapshot it is exactly the live-only behaviour: one fail-fast
// fetch, refresh for the life of the process, refuse to start on failure.
func newJWKSKeyfunc(cfg JWTConfig) (jwt.Keyfunc, func(), error) {
	resolve := cfg.resolver()
	ctx, cancel := context.WithCancel(context.Background())

	if !cfg.JWKSSnapshot.configured() {
		live, lcancel, err := attemptLive(ctx, resolve, 0)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		return live.kf.Keyfunc, func() { lcancel(); cancel() }, nil
	}

	snap := cfg.JWKSSnapshot
	if snap.Issuer == "" {
		snap.Issuer = cfg.Issuer
	}
	f := &fallbackKeys{
		snap: newSnapshotSource(snap, slog.Default().With("component", "auth-jwks"), snapshotFileRecheck),
		log:  slog.Default().With("component", "auth-jwks"),
	}

	bootAttempt := jwksBootAttempt
	live, lcancel, liveErr := attemptLive(ctx, resolve, bootAttempt)
	if liveErr == nil {
		f.live.Store(live)
		return f.keyfunc, func() { lcancel(); cancel() }, nil
	}

	// The issuer is unreachable. Start only when a usable snapshot says what
	// its keys were; otherwise this is "no keys at all" and the server must
	// not boot.
	if _, err := f.snap.get(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("auth: issuer JWKS unreachable (%v) and no usable snapshot: %w", liveErr, err)
	}
	f.log.Warn("issuer JWKS unreachable at boot; starting on the delivered JWKS snapshot and retrying the issuer in the background",
		"error", liveErr, "max_age", snap.maxAge().String())
	go f.retryLive(ctx, resolve, jwksRetryInitial, jwksRetryMax, jwksBootAttempt)
	return f.keyfunc, cancel, nil
}

// fallbackKeys is the key source used when a snapshot is configured.
type fallbackKeys struct {
	snap *snapshotSource
	log  *slog.Logger

	live        atomic.Pointer[liveJWKS]
	unionLogged sync.Map // kid -> struct{}
}

// retryLive keeps trying to reach the issuer until it can, then installs the
// live source. From that moment the snapshot only serves the rotation-overlap
// exception.
func (f *fallbackKeys) retryLive(ctx context.Context, resolve jwksResolver, initial, maxDelay, attempt time.Duration) {
	delay := initial
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		live, _, err := attemptLive(ctx, resolve, attempt)
		if err == nil {
			// Its refresh goroutine is bound to ctx, which the validator's
			// stop func cancels.
			f.live.Store(live)
			f.log.InfoContext(ctx, "issuer JWKS reachable again; now verifying against live keys (snapshot retained only for rotation overlap)")
			return
		}
		f.log.WarnContext(ctx, "issuer JWKS still unreachable; continuing on the JWKS snapshot",
			"error", err, "retry_in", min(delay*2, maxDelay).String())
		delay = min(delay*2, maxDelay)
	}
}

func (f *fallbackKeys) keyfunc(tok *jwt.Token) (any, error) {
	live := f.live.Load()
	if live == nil {
		snap, err := f.snap.get()
		if err != nil {
			return nil, fmt.Errorf("auth: issuer JWKS unreachable and %w", err)
		}
		return snap.keys.Keyfunc(tok)
	}

	// Live is reachable. A kid it holds is answered by live, always: live wins.
	kid, _ := tok.Header["kid"].(string)
	if kid == "" || live.holds(context.Background(), kid) {
		return live.kf.Keyfunc(tok)
	}

	// A kid live does not hold. The snapshot may answer it only if it is newer
	// than the live set this process last fetched (a rotation the publisher
	// saw and this process has not refreshed for) and actually holds the kid.
	// Otherwise live's own answer stands, including its refresh of an unknown
	// kid.
	if snap, err := f.snap.get(); err == nil && snap.deliveredAt.After(live.lastFetch()) {
		if key, kerr := snap.keys.Keyfunc(tok); kerr == nil {
			if _, seen := f.unionLogged.LoadOrStore(kid, struct{}{}); !seen {
				f.log.Warn("accepted a token signed with a key only the JWKS snapshot holds (the issuer's live set has not caught up with a rotation)",
					"kid", kid, "snapshot_delivered_at", snap.deliveredAt.Format(time.RFC3339))
			}
			return key, nil
		}
	}
	return live.kf.Keyfunc(tok)
}

// snapshotSource loads, validates and caches the configured snapshot.
type snapshotSource struct {
	cfg     JWKSSnapshot
	log     *slog.Logger
	now     func() time.Time
	recheck time.Duration

	mu      sync.Mutex
	checked time.Time
	file    *loadedSnapshot
	fileErr error
	fileRaw []byte
	inline  *loadedSnapshot
	inlErr  error
}

// loadedSnapshot is one parsed, structurally valid snapshot.
type loadedSnapshot struct {
	deliveredAt time.Time
	keys        keyfunc.Keyfunc
	source      string
}

func newSnapshotSource(cfg JWKSSnapshot, log *slog.Logger, recheck time.Duration) *snapshotSource {
	s := &snapshotSource{cfg: cfg, log: log, now: time.Now, recheck: recheck}
	if cfg.Inline != "" {
		s.inline, s.inlErr = s.parse([]byte(cfg.Inline), time.Time{}, "config value")
		if s.inlErr != nil {
			log.Warn("JWKS snapshot config value is unusable", "error", s.inlErr)
		}
	}
	return s
}

// get returns the newest usable snapshot: structurally valid, not dated in the
// future, and no older than MaxAge. The error says why there is none.
func (s *snapshotSource) get() (*loadedSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cfg.File != "" && (s.checked.IsZero() || now.Sub(s.checked) >= s.recheck) {
		s.checked = now
		s.refreshFileLocked()
	}

	var best *loadedSnapshot
	var reasons []error
	for _, c := range []struct {
		snap *loadedSnapshot
		err  error
	}{{s.file, s.fileErr}, {s.inline, s.inlErr}} {
		switch {
		case c.err != nil:
			reasons = append(reasons, c.err)
		case c.snap != nil && (best == nil || c.snap.deliveredAt.After(best.deliveredAt)):
			best = c.snap
		}
	}
	if best == nil {
		if len(reasons) == 0 {
			return nil, errors.New("no JWKS snapshot is configured")
		}
		return nil, fmt.Errorf("no usable JWKS snapshot: %w", errors.Join(reasons...))
	}
	if age := now.Sub(best.deliveredAt); age > s.cfg.maxAge() {
		return nil, fmt.Errorf("the JWKS snapshot (%s) is stale: delivered %s ago, max age %s",
			best.source, age.Round(time.Second), s.cfg.maxAge())
	}
	return best, nil
}

func (s *snapshotSource) refreshFileLocked() {
	raw, err := os.ReadFile(s.cfg.File)
	if err != nil {
		s.file, s.fileRaw = nil, nil
		s.fileErr = fmt.Errorf("read JWKS snapshot file: %w", err)
		return
	}
	if s.fileErr == nil && s.file != nil && bytes.Equal(raw, s.fileRaw) {
		return
	}
	var mtime time.Time
	if fi, err := os.Stat(s.cfg.File); err == nil {
		mtime = fi.ModTime()
	}
	snap, err := s.parse(raw, mtime, "file "+s.cfg.File)
	s.fileRaw = raw
	if err != nil {
		s.file, s.fileErr = nil, err
		s.log.Warn("JWKS snapshot file is unusable", "path", s.cfg.File, "error", err)
		return
	}
	if s.file != nil || s.fileErr != nil {
		s.log.Info("JWKS snapshot file reloaded", "path", s.cfg.File, "delivered_at", snap.deliveredAt.Format(time.RFC3339))
	}
	s.file, s.fileErr = snap, nil
}

type snapshotDoc struct {
	Issuer      string            `json:"issuer"`
	DeliveredAt time.Time         `json:"delivered_at"`
	Keys        []json.RawMessage `json:"keys"`
}

// parse validates a snapshot document. fallbackTime dates a document that
// carries no delivered_at (a file's mtime); the zero time means there is none.
func (s *snapshotSource) parse(raw []byte, fallbackTime time.Time, source string) (*loadedSnapshot, error) {
	var doc snapshotDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("JWKS snapshot (%s) is not valid JSON: %w", source, err)
	}
	if len(doc.Keys) == 0 {
		return nil, fmt.Errorf("JWKS snapshot (%s) holds no keys", source)
	}
	if s.cfg.Issuer != "" && doc.Issuer != "" && doc.Issuer != s.cfg.Issuer {
		return nil, fmt.Errorf("JWKS snapshot (%s) was published for issuer %q, not %q", source, doc.Issuer, s.cfg.Issuer)
	}
	delivered := doc.DeliveredAt
	if delivered.IsZero() {
		delivered = fallbackTime
	}
	if delivered.IsZero() {
		return nil, fmt.Errorf("JWKS snapshot (%s) has no delivered_at, so its age cannot be judged", source)
	}
	if delivered.After(s.now().Add(snapshotFutureSkew)) {
		return nil, fmt.Errorf("JWKS snapshot (%s) is dated %s, in the future", source, delivered.Format(time.RFC3339))
	}
	kf, err := keyfunc.NewJWKSetJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("JWKS snapshot (%s) holds no usable keys: %w", source, err)
	}
	return &loadedSnapshot{deliveredAt: delivered, keys: kf, source: source}, nil
}

// JWKSKeyfuncWithSnapshot is [JWKSKeyfunc] for one URL with a [JWKSSnapshot]
// fallback, for callers composing their own validators (for example through
// [Config.TokenValidators]). The returned func stops the background retry and
// refresh; call it when the validator is retired. See the package JWKS
// snapshot notes for what is trusted and when.
func JWKSKeyfuncWithSnapshot(url string, snap JWKSSnapshot) (jwt.Keyfunc, func(), error) {
	if url == "" {
		return nil, nil, errors.New("auth: JWKSKeyfuncWithSnapshot requires a JWKS URL")
	}
	return newJWKSKeyfunc(JWTConfig{JWKSURL: url, JWKSSnapshot: snap})
}
