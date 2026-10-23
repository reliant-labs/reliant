package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdempotencyMiddleware_NoKey(t *testing.T) {
	var calls int32
	handler := IdempotencyMiddleware(IdempotencyConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Request without Idempotency-Key passes through every time.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 handler invocations, got %d", got)
	}
}

func TestIdempotencyMiddleware_DuplicateKey(t *testing.T) {
	var calls int32
	handler := IdempotencyMiddleware(IdempotencyConfig{CacheSize: 10, TTL: time.Minute})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("X-Custom", "val")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Idempotency-Key", "abc-123")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated {
			t.Fatalf("iteration %d: expected 201, got %d", i, rr.Code)
		}
		body, _ := io.ReadAll(rr.Body)
		if string(body) != "created" {
			t.Fatalf("iteration %d: expected body 'created', got %q", i, body)
		}
		if rr.Header().Get("X-Custom") != "val" {
			t.Fatalf("iteration %d: missing X-Custom header", i)
		}
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 handler invocation (cached), got %d", got)
	}
}

func TestIdempotencyMiddleware_TTLExpiry(t *testing.T) {
	var calls int32
	handler := IdempotencyMiddleware(IdempotencyConfig{CacheSize: 10, TTL: 1 * time.Millisecond})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "expire-me")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	time.Sleep(5 * time.Millisecond) // Wait for TTL expiry.

	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.Header.Set("Idempotency-Key", "expire-me")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 handler invocations after TTL expiry, got %d", got)
	}
}

// TestIdempotencyMiddleware_DefaultsUnchanged pins the pre-Key/ShouldCache
// contract: a config that sets only CacheSize/TTL still keys off the
// Idempotency-Key header and still caches EVERY status, 5xx included.
// Callers written against the old struct must not change behaviour just
// because two optional function fields appeared next to theirs.
func TestIdempotencyMiddleware_DefaultsUnchanged(t *testing.T) {
	var calls int32
	handler := IdempotencyMiddleware(IdempotencyConfig{CacheSize: 10, TTL: time.Minute})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Idempotency-Key", "same-key")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("iteration %d: got %d, want 500", i, rr.Code)
		}
		body, _ := io.ReadAll(rr.Body)
		if string(body) != "boom" {
			t.Fatalf("iteration %d: body = %q, want \"boom\"", i, body)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler invocations = %d, want 1 — the default still caches 5xx", got)
	}

	// And a request without the header is still never deduplicated.
	before := atomic.LoadInt32(&calls)
	for i := 0; i < 2; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	}
	if got := atomic.LoadInt32(&calls) - before; got != 2 {
		t.Fatalf("keyless invocations = %d, want 2", got)
	}
}

// TestIdempotencyMiddleware_CustomKey — a webhook sender never supplies
// Idempotency-Key; it stamps its own delivery ID.
func TestIdempotencyMiddleware_CustomKey(t *testing.T) {
	var calls int32
	cfg := IdempotencyConfig{
		CacheSize: 10,
		TTL:       time.Minute,
		Key:       func(r *http.Request) string { return WebhookEventID(r.Header) },
	}
	handler := IdempotencyMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", nil)
		req.Header.Set("Stripe-Event-Id", "evt_1Pabc")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler invocations = %d, want 1 (deduplicated on Stripe-Event-Id)", got)
	}

	// An Idempotency-Key header is now irrelevant — Key owns the decision.
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", nil)
	req.Header.Set("Idempotency-Key", "evt_1Pabc")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler invocations = %d, want 2 — Key returned \"\", so no dedup", got)
	}
}

// TestIdempotencyMiddleware_ShouldCache is the retry-killing case: with a
// 2xx-only predicate a failed request is NOT cached, so the sender's
// retry reaches the handler instead of replaying the failure for the TTL.
func TestIdempotencyMiddleware_ShouldCache(t *testing.T) {
	var calls int32
	status := http.StatusInternalServerError
	cfg := IdempotencyConfig{
		CacheSize:   10,
		TTL:         time.Minute,
		ShouldCache: func(code int) bool { return code < 400 },
	}
	handler := IdempotencyMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(status)
	}))

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Idempotency-Key", "retry-me")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	// Two failures: neither is cached, so both reach the handler.
	if code := send(); code != http.StatusInternalServerError {
		t.Fatalf("first attempt = %d, want 500", code)
	}
	if code := send(); code != http.StatusInternalServerError {
		t.Fatalf("retry = %d, want 500", code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler invocations = %d, want 2 — a cached 5xx would have killed the retry", got)
	}

	// The next retry succeeds and IS cached, so the one after replays it.
	status = http.StatusOK
	if code := send(); code != http.StatusOK {
		t.Fatalf("successful retry = %d, want 200", code)
	}
	if code := send(); code != http.StatusOK {
		t.Fatalf("duplicate after success = %d, want 200", code)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("handler invocations = %d, want 3 — the 200 must be cached", got)
	}
}

func TestIdempotencyMiddleware_LRUEviction(t *testing.T) {
	var calls int32
	handler := IdempotencyMiddleware(IdempotencyConfig{CacheSize: 2, TTL: time.Minute})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))

	// Fill cache with 2 entries.
	for _, key := range []string{"a", "b"} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Idempotency-Key", key)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Insert a 3rd — should evict "a".
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "c")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// "a" should now miss (handler invoked again).
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.Header.Set("Idempotency-Key", "a")
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	// 3 initial + 1 re-invocation of "a" = 4
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("expected 4 handler invocations, got %d", got)
	}
}
