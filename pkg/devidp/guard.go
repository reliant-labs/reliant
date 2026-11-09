package devidp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Request guards the sign-in routes share: cross-site request refusal, the
// client address rate limits are keyed by, the limiters themselves, and a
// response-time floor for the routes whose work differs by account state.

// ── Cross-site requests ──────────────────────────────────────────────────

// NewCrossOriginProtection refuses a cross-site state-changing request to
// the sign-in routes: a page on another site that POSTs (even as text/plain,
// which needs no CORS preflight) to /auth/register or /auth/login would
// otherwise create accounts or sign the victim's browser into the attacker's
// account. It is net/http's CrossOriginProtection (Sec-Fetch-Site, else
// Origin against Host), trusting the app's own public origin as well, because
// the frontend may be served from a different origin than the API.
//
// EXACT ORIGIN, not site: Sec-Fetch-Site must be same-origin (or none), and
// any other value passes only with an Origin equal to the app's own origin.
// same-site is refused like cross-site, because sibling sites under a shared
// parent that is not on the Public Suffix List (hosted forge sites are all
// <name>.sites.reliantlabs.dev) are same-site with each other, and a
// malicious sibling must not be able to post here. Without Sec-Fetch-Site
// (an old browser) the Origin must name this host or the app's origin.
//
// Requests with neither header (curl, server-to-server) pass: they carry no
// browser's cookies, which is what a cross-site request forgery abuses.
func NewCrossOriginProtection(appBase string) *http.CrossOriginProtection {
	p := http.NewCrossOriginProtection()
	if u, err := url.Parse(strings.TrimSpace(appBase)); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		_ = p.AddTrustedOrigin(u.Scheme + "://" + u.Host)
	}
	p.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusForbidden, CodeCrossSite, "That request came from another site.")
	}))
	return p
}

// GuardedMux mounts every handler behind a CrossOriginProtection. The
// scaffolded broker mounts its routes through one, so no route can be added
// unguarded by forgetting a wrapper.
type GuardedMux struct {
	mux   *http.ServeMux
	guard *http.CrossOriginProtection
}

// GuardMux wraps mux so that every Handle is cross-site protected, trusting
// appBase's origin.
func GuardMux(mux *http.ServeMux, appBase string) *GuardedMux {
	return &GuardedMux{mux: mux, guard: NewCrossOriginProtection(appBase)}
}

// Handle mounts h on pattern behind the guard.
func (g *GuardedMux) Handle(pattern string, h http.Handler) {
	g.mux.Handle(pattern, g.guard.Handler(h))
}

// ── Client address ───────────────────────────────────────────────────────

// ClientIP is the address to rate-limit a request by.
//
// trustedHops is how many reverse proxies in front of this server each append
// the address they received the request from to X-Forwarded-For. 0 (the
// default) ignores the header and uses the connection's peer: a header the
// client sets is a header an attacker rotates for a fresh budget per request.
// With N, the Nth address from the RIGHT is the one the outermost trusted
// proxy saw — everything left of it is the client's to forge. A header with
// fewer entries than N, or an entry that is not an IP, falls back to the peer.
func ClientIP(r *http.Request, trustedHops int) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if trustedHops <= 0 {
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				hops = append(hops, p)
			}
		}
	}
	if len(hops) < trustedHops {
		return peer
	}
	if ip := net.ParseIP(hops[len(hops)-trustedHops]); ip != nil {
		return ip.String()
	}
	return peer
}

// ClientKey is the key a per-client rate limit counts under: ClientIP, with
// an IPv6 address cut to its /64. One subscriber is routinely handed a whole
// /64, so a key per full address would give them 2^64 fresh budgets.
func ClientKey(r *http.Request, trustedHops int) string {
	ip := ClientIP(r, trustedHops)
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ── Limiters ─────────────────────────────────────────────────────────────

// limiter is a fixed-window attempt budget per key, in memory: the same
// trade-off as the broker's attemptLimiter (N replicas, N times the budget).
//
// Memory is bounded at limiterMaxKeys. A full table drops expired windows,
// then EVICTS THE OLDEST live one rather than refusing the new key: refusing
// would let anyone who fills the table with made-up keys (user ids are the
// caller's to choose) lock every real user out. Eviction resets one budget
// instead, which is why every per-user budget sits behind a per-IP one: a
// caller must spend limiterMaxKeys attempts, from many addresses, to evict a
// target's entry.
type limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	now     func() time.Time
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	used  int
	since time.Time
}

const limiterMaxKeys = 10_000

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, now: time.Now, entries: map[string]*limiterEntry{}}
}

func (l *limiter) reserve(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[key]
	if e == nil || now.Sub(e.since) > l.window {
		if e == nil && len(l.entries) >= limiterMaxKeys {
			l.makeRoom(now)
		}
		e = &limiterEntry{since: now}
		l.entries[key] = e
	}
	if e.used >= l.max {
		return false
	}
	e.used++
	return true
}

// makeRoom drops every expired window, and the oldest live one if none had
// expired.
func (l *limiter) makeRoom(now time.Time) {
	var oldest string
	var oldestSince time.Time
	for k, v := range l.entries {
		if now.Sub(v.since) > l.window {
			delete(l.entries, k)
			continue
		}
		if oldest == "" || v.since.Before(oldestSince) {
			oldest, oldestSince = k, v.since
		}
	}
	if len(l.entries) >= limiterMaxKeys && oldest != "" {
		delete(l.entries, oldest)
	}
}

func (l *limiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[key]; e != nil && e.used > 0 {
		e.used--
	}
}

func (l *limiter) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// ── Response-time floor ──────────────────────────────────────────────────

// DefaultResponseFloor is how long HandleMagicRequest and HandlePasskeyBegin
// take at least. Their work differs by account state — an unknown address
// costs one lookup, a known one a lookup, a method check and a challenge —
// and without a floor the difference in response time says which addresses
// have accounts. It is set above what the slower path takes against a hosted
// instance; work that overruns it still leaks, so raise it if yours does.
const DefaultResponseFloor = 1200 * time.Millisecond

// holdUntil sleeps until start+floor plus up to 10% jitter, or until ctx ends.
func holdUntil(ctx context.Context, start time.Time, floor time.Duration) {
	if floor <= 0 {
		return
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	jitter := time.Duration(binary.BigEndian.Uint64(b[:]) % uint64(floor/10+1))
	wait := time.Until(start.Add(floor + jitter))
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
