package middleware

import (
	"context"
	"fmt"
	"net"
	"sync"

	"connectrpc.com/connect"
	"golang.org/x/time/rate"

	"github.com/reliant-labs/forge/pkg/auth"
)

// rateLimitCacheSize caps the number of distinct keys tracked by the
// interceptor. Exceeding this evicts the least-recently-used key. The cap
// keeps memory bounded under key-cardinality attacks (e.g. spoofed IPs).
const rateLimitCacheSize = 10_000

// RateLimitOptions tunes the per-key token-bucket rate limiter.
//
//   - Rps:   steady-state requests-per-second per key
//   - Burst: maximum burst allowed before throttling (raised to Rps when lower)
//
// Rps <= 0 disables rate limiting — RateLimitInterceptor returns nil in that
// case and callers should skip appending the interceptor to the chain.
type RateLimitOptions struct {
	Rps   int
	Burst int
}

// ClaimsLookup resolves the authenticated principal from a request
// context. The claims context key is owned by the project's
// pkg/middleware, so claims-aware interceptors in this library take the
// project's ClaimsFromContext as a callback. nil is allowed and means
// "no claims available" (the interceptors degrade gracefully).
type ClaimsLookup func(ctx context.Context) (*auth.Claims, bool)

// RateLimitInterceptor returns a Connect interceptor that enforces a
// per-key token-bucket rate limit. Keys are derived in this order:
//  1. authenticated claim subject (claims.UserID) via claimsFrom, if available
//  2. peer IP from the Connect request/stream peer address
//
// When opts.Rps <= 0 the interceptor is disabled and this function returns
// nil. Memory is bounded by an LRU cache of up to rateLimitCacheSize
// limiters; idle keys are evicted in LRU order so the interceptor is safe
// to expose to anonymous traffic. claimsFrom may be nil (all callers are
// then keyed by peer IP).
func RateLimitInterceptor(opts RateLimitOptions, claimsFrom ClaimsLookup) connect.Interceptor {
	if opts.Rps <= 0 {
		return nil
	}
	if opts.Burst < opts.Rps {
		opts.Burst = opts.Rps
	}
	return &rateLimitInterceptor{
		limit:      rate.Limit(opts.Rps),
		burst:      opts.Burst,
		cache:      newLRUCache[*rate.Limiter](rateLimitCacheSize),
		claimsFrom: claimsFrom,
	}
}

type rateLimitInterceptor struct {
	mu         sync.Mutex // guards cache (get+add must be atomic together)
	limit      rate.Limit
	burst      int
	cache      *lruCache[*rate.Limiter]
	claimsFrom ClaimsLookup
}

func (i *rateLimitInterceptor) limiterFor(key string) *rate.Limiter {
	i.mu.Lock()
	defer i.mu.Unlock()
	if l, ok := i.cache.get(key); ok {
		return l
	}
	l := rate.NewLimiter(i.limit, i.burst)
	i.cache.add(key, l)
	return l
}

func (i *rateLimitInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		key := i.rateLimitKey(ctx, req.Peer().Addr)
		if !i.limiterFor(key).Allow() {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("rate limit exceeded"))
		}
		return next(ctx, req)
	}
}

func (i *rateLimitInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *rateLimitInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		key := i.rateLimitKey(ctx, conn.Peer().Addr)
		if !i.limiterFor(key).Allow() {
			return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("rate limit exceeded"))
		}
		return next(ctx, conn)
	}
}

// rateLimitKey returns the cache key for a request. Authenticated callers
// are keyed by claim subject so a single user can't escape limiting by
// rotating source IPs. Unauthenticated callers are keyed by peer IP only
// (port is stripped so a single client with many ephemeral ports counts as
// one key).
func (i *rateLimitInterceptor) rateLimitKey(ctx context.Context, peerAddr string) string {
	if i.claimsFrom != nil {
		if claims, ok := i.claimsFrom(ctx); ok && claims != nil && claims.UserID != "" {
			return "sub:" + claims.UserID
		}
	}
	return "ip:" + peerIPOnly(peerAddr)
}

// peerIPOnly strips the port from a "host:port" peer address. Unparseable
// inputs are returned unchanged; the caller treats them as opaque strings.
func peerIPOnly(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
