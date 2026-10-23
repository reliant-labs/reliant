package middleware

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// IdempotencyConfig controls the behavior of the idempotency middleware.
//
// The zero value is a working default: 1000 keys, 1h TTL, keyed off the
// Idempotency-Key header, caching every completed response.
type IdempotencyConfig struct {
	// CacheSize is the maximum number of idempotency keys to track.
	// Default: 1000.
	CacheSize int

	// TTL is how long a cached response is kept. After this duration the
	// key is evicted and a duplicate request is treated as new.
	// Default: 1 hour.
	TTL time.Duration

	// Key derives the deduplication key from the inbound request.
	// Returning "" opts that request out of deduplication entirely: it is
	// passed straight through and its response is never cached.
	//
	// nil reads the IdempotencyKeyHeader — the client-driven convention
	// REST/RPC callers follow. Supply a Key when the sender does not
	// control that header: webhook providers each stamp their own event ID
	// (Stripe-Event-Id, X-GitHub-Delivery, …), and callback routes may key
	// off a path value.
	//
	//	cfg.Key = func(r *http.Request) string {
	//	    return middleware.WebhookEventID(r.Header)
	//	}
	Key func(*http.Request) string

	// ShouldCache decides, from the status code the handler produced,
	// whether that response is stored for replay. nil caches every
	// completed response.
	//
	// Supply a predicate whenever the sender RETRIES on failure. Caching a
	// 500 replays that 500 for the whole TTL, so every retry the sender
	// makes is answered from the cache and the request is never actually
	// reprocessed:
	//
	//	cfg.ShouldCache = func(status int) bool { return status < 400 }
	ShouldCache func(statusCode int) bool
}

func (c *IdempotencyConfig) cacheSize() int {
	if c.CacheSize > 0 {
		return c.CacheSize
	}
	return 1000
}

func (c *IdempotencyConfig) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return time.Hour
}

// key resolves the deduplication key for r, defaulting to the
// Idempotency-Key header when no Key function is configured.
func (c *IdempotencyConfig) key(r *http.Request) string {
	if c.Key != nil {
		return c.Key(r)
	}
	return r.Header.Get(IdempotencyKeyHeader)
}

// shouldCache reports whether a response with the given status is stored
// for replay, defaulting to caching everything.
func (c *IdempotencyConfig) shouldCache(statusCode int) bool {
	if c.ShouldCache == nil {
		return true
	}
	return c.ShouldCache(statusCode)
}

type cachedResponse struct {
	statusCode int
	header     http.Header
	body       []byte
	expiresAt  time.Time
}

// IdempotencyMiddleware returns an http.Handler that deduplicates requests
// carrying the same key. If a request with that key has already been served
// (and is still cached), the cached response is replayed without invoking
// the downstream handler.
//
// By default the key is the Idempotency-Key header and every completed
// response is cached; IdempotencyConfig.Key and IdempotencyConfig.ShouldCache
// override each half independently.
//
// Usage:
//
//	mux := http.NewServeMux()
//	cfg := middleware.IdempotencyConfig{CacheSize: 500, TTL: 30 * time.Minute}
//	handler := middleware.IdempotencyMiddleware(cfg)(mux)
//
// The cache is bounded (LRU over CacheSize entries) and PER-PROCESS: two
// replicas each keep their own, so a duplicate routed to the other replica
// is served fresh. Replay also happens BEFORE the wrapped handler runs, so
// a route that authenticates inside the handler (a webhook verifying its
// signature) answers unauthenticated duplicates from the cache. When either
// property matters, deduplicate inside the handler instead — see
// [DedupeStore] for the boolean-shaped seam that swaps for a table.
func IdempotencyMiddleware(cfg IdempotencyConfig) func(http.Handler) http.Handler {
	ttl := cfg.ttl()

	var mu sync.Mutex
	cache := newLRUCache[*cachedResponse](cfg.cacheSize())

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := cfg.key(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			mu.Lock()
			entry, hit := cache.get(key)
			mu.Unlock()
			if hit && time.Now().Before(entry.expiresAt) {
				for k, vs := range entry.header {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(entry.statusCode)
				_, _ = w.Write(entry.body)
				return
			}

			// Cache miss — execute handler and capture response.
			rec := &responseRecorder{
				header: make(http.Header),
				body:   &bytes.Buffer{},
				code:   http.StatusOK,
			}
			next.ServeHTTP(rec, r)

			// Write the captured response to the real writer.
			for k, vs := range rec.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.code)
			_, _ = w.Write(rec.body.Bytes())

			if !cfg.shouldCache(rec.code) {
				return
			}

			mu.Lock()
			cache.add(key, &cachedResponse{
				statusCode: rec.code,
				header:     rec.header.Clone(),
				body:       rec.body.Bytes(),
				expiresAt:  time.Now().Add(ttl),
			})
			mu.Unlock()
		})
	}
}

// responseRecorder captures an HTTP response for caching.
type responseRecorder struct {
	header http.Header
	body   *bytes.Buffer
	code   int
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }

func (r *responseRecorder) WriteHeader(code int) { r.code = code }
