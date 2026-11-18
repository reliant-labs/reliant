package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"connectrpc.com/connect"
)

// IdempotencyKeyHeader is the canonical header carrying the
// client-supplied idempotency key. Methods annotated with
// idempotency_key=true in the proto options SHOULD require this header;
// if absent the request proceeds without deduplication.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyOptions configures the RPC idempotency interceptor.
//
//   - CacheSize: maximum number of cached responses (default 1000)
//   - TTL:       how long a cached response is valid (default 1h)
//
// CacheSize <= 0 disables the interceptor — IdempotencyInterceptor
// returns nil and callers should skip appending it to the chain.
type IdempotencyOptions struct {
	CacheSize int
	TTL       time.Duration
}

type cachedRPCResponse struct {
	resp      connect.AnyResponse
	err       error
	expiresAt time.Time
}

type idempotencyInterceptor struct {
	mu    sync.Mutex
	cache *lruCache[*cachedRPCResponse]
	ttl   time.Duration
}

// IdempotencyInterceptor returns a Connect interceptor that deduplicates
// requests carrying an Idempotency-Key header. If the same key is seen
// within the TTL window, the cached response (or error) is returned
// without calling the handler again.
//
// Keys are scoped per-procedure so the same key on different RPCs is
// treated as distinct. For the HTTP-layer equivalent (REST/webhook
// routes) see [IdempotencyMiddleware].
//
// When opts.CacheSize <= 0 the interceptor is disabled and nil is returned.
func IdempotencyInterceptor(opts IdempotencyOptions) connect.Interceptor {
	if opts.CacheSize <= 0 {
		return nil
	}
	if opts.TTL <= 0 {
		opts.TTL = time.Hour
	}
	return &idempotencyInterceptor{cache: newLRUCache[*cachedRPCResponse](opts.CacheSize), ttl: opts.TTL}
}

func (i *idempotencyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		key := req.Header().Get(IdempotencyKeyHeader)
		if key == "" {
			return next(ctx, req)
		}
		cacheKey := idempotencyCacheKey(req.Spec().Procedure, key)

		i.mu.Lock()
		if cached, ok := i.cache.get(cacheKey); ok && time.Now().Before(cached.expiresAt) {
			i.mu.Unlock()
			return cached.resp, cached.err
		}
		i.mu.Unlock()

		resp, err := next(ctx, req)

		i.mu.Lock()
		i.cache.add(cacheKey, &cachedRPCResponse{
			resp:      resp,
			err:       err,
			expiresAt: time.Now().Add(i.ttl),
		})
		i.mu.Unlock()

		return resp, err
	}
}

func (i *idempotencyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *idempotencyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next // idempotency is only meaningful for unary RPCs
}

// idempotencyCacheKey combines procedure and client key into a single cache
// key. Using a hash avoids unbounded key length from malicious clients.
func idempotencyCacheKey(procedure, clientKey string) string {
	h := sha256.Sum256([]byte(procedure + "\x00" + clientKey))
	return hex.EncodeToString(h[:])
}
