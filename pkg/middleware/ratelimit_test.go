package middleware

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/auth"
)

// Test claims stash standing in for the project-owned context key.
type rlClaimsKey struct{}

func rlContextWithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, rlClaimsKey{}, claims)
}

func rlClaimsFromContext(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(rlClaimsKey{}).(*auth.Claims)
	return c, ok
}

func TestRateLimitInterceptor_Disabled(t *testing.T) {
	if got := RateLimitInterceptor(RateLimitOptions{Rps: 0, Burst: 0}, nil); got != nil {
		t.Fatalf("expected nil interceptor when Rps<=0, got %T", got)
	}
	if got := RateLimitInterceptor(RateLimitOptions{Rps: -1, Burst: 10}, nil); got != nil {
		t.Fatalf("expected nil interceptor when Rps<0, got %T", got)
	}
}

func TestRateLimitInterceptor_EnforcesBurst(t *testing.T) {
	// Very low steady rate + small burst so we can observe rejection
	// without any time dependency. Burst=2 allows two Allow() calls
	// before the token bucket drains.
	ic := RateLimitInterceptor(RateLimitOptions{Rps: 1, Burst: 2}, nil)
	if ic == nil {
		t.Fatal("expected non-nil interceptor when Rps>0")
	}
	rl, ok := ic.(*rateLimitInterceptor)
	if !ok {
		t.Fatalf("unexpected interceptor type %T", ic)
	}

	key := "ip:203.0.113.1"
	l := rl.limiterFor(key)

	if !l.Allow() {
		t.Fatal("first request should be allowed")
	}
	if !l.Allow() {
		t.Fatal("second request should be allowed (within burst)")
	}
	if l.Allow() {
		t.Fatal("third request should be throttled (burst exhausted)")
	}
}

func TestRateLimitInterceptor_PerKeyIsolation(t *testing.T) {
	// Two distinct keys should have independent limiters: exhausting one
	// must not affect the other.
	ic := RateLimitInterceptor(RateLimitOptions{Rps: 1, Burst: 1}, nil)
	rl := ic.(*rateLimitInterceptor)

	a := rl.limiterFor("ip:198.51.100.1")
	b := rl.limiterFor("ip:198.51.100.2")

	if !a.Allow() {
		t.Fatal("key a first call should be allowed")
	}
	if a.Allow() {
		t.Fatal("key a second call should be throttled")
	}
	if !b.Allow() {
		t.Fatal("key b first call should be allowed even after a is exhausted")
	}
}

func TestRateLimitKey_PrefersClaimSubject(t *testing.T) {
	ic := RateLimitInterceptor(RateLimitOptions{Rps: 1, Burst: 1}, rlClaimsFromContext)
	rl := ic.(*rateLimitInterceptor)
	ctx := rlContextWithClaims(context.Background(), &auth.Claims{UserID: "user-123"})
	if got := rl.rateLimitKey(ctx, "203.0.113.9:443"); got != "sub:user-123" {
		t.Fatalf("expected sub key for authenticated request, got %q", got)
	}
}

func TestRateLimitKey_FallsBackToPeerIP(t *testing.T) {
	ic := RateLimitInterceptor(RateLimitOptions{Rps: 1, Burst: 1}, rlClaimsFromContext)
	rl := ic.(*rateLimitInterceptor)
	ctx := context.Background()
	if got := rl.rateLimitKey(ctx, "203.0.113.9:443"); got != "ip:203.0.113.9" {
		t.Fatalf("expected ip key stripped of port, got %q", got)
	}
	// Unparseable peer addresses must not panic and must be used as-is.
	if got := rl.rateLimitKey(ctx, "bogus"); got != "ip:bogus" {
		t.Fatalf("expected ip:bogus for unparseable addr, got %q", got)
	}
	if got := rl.rateLimitKey(ctx, ""); got != "ip:" {
		t.Fatalf("expected ip: for empty addr, got %q", got)
	}
}

// A nil ClaimsLookup must not panic — everything keys by peer IP.
func TestRateLimitKey_NilClaimsLookup(t *testing.T) {
	ic := RateLimitInterceptor(RateLimitOptions{Rps: 1, Burst: 1}, nil)
	rl := ic.(*rateLimitInterceptor)
	if got := rl.rateLimitKey(context.Background(), "203.0.113.9:443"); got != "ip:203.0.113.9" {
		t.Fatalf("expected ip key with nil lookup, got %q", got)
	}
}

// The LRU bound must hold: inserting more keys than the cap evicts the
// least-recently-used limiter instead of growing without bound.
func TestRateLimit_LRUBound(t *testing.T) {
	c := newLRUCache[int](3)
	c.add("a", 1)
	c.add("b", 2)
	c.add("c", 3)
	c.get("a") // refresh a → b is now LRU
	c.add("d", 4)
	if c.len() != 3 {
		t.Fatalf("cache must stay at cap, got %d", c.len())
	}
	if _, ok := c.get("b"); ok {
		t.Fatal("LRU entry b should have been evicted")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("recently-used entry a must survive eviction")
	}
}
