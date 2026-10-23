package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDMiddleware_GeneratesWhenAbsent(t *testing.T) {
	t.Parallel()

	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := RequestIDMiddleware()(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got == "" {
		t.Fatal("request id must be generated when absent")
	}
	if resp := rec.Header().Get(RequestIDHeader); resp != got {
		t.Fatalf("response header must echo context id: want %q got %q", got, resp)
	}
	// Generated IDs are 16 random bytes hex-encoded (32 chars). We don't
	// hardcode the exact value; we do assert the result is non-trivially
	// long so a regression to, say, "req-000" fails loudly.
	if len(got) < 20 {
		t.Fatalf("generated id looks truncated: %q", got)
	}
}

func TestRequestIDMiddleware_PreservesInbound(t *testing.T) {
	t.Parallel()

	const inbound = "client-supplied-id-123"
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := RequestIDMiddleware()(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(RequestIDHeader, inbound)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got != inbound {
		t.Fatalf("context id: want %q got %q", inbound, got)
	}
	if resp := rec.Header().Get(RequestIDHeader); resp != inbound {
		t.Fatalf("response header: want %q got %q", inbound, resp)
	}
}

func TestRequestIDMiddleware_UniqueAcrossRequests(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 8)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[RequestIDFromContext(r.Context())] = struct{}{}
		w.WriteHeader(http.StatusOK)
	})
	h := RequestIDMiddleware()(next)

	for i := 0; i < 8; i++ {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if len(seen) != 8 {
		t.Fatalf("expected 8 unique ids, got %d", len(seen))
	}
}

func TestRequestIDFromContext_Empty(t *testing.T) {
	t.Parallel()
	// Background ctx with no request ID attached must return "" and
	// never panic — middleware-less call sites (e.g. a CLI tool reusing
	// the same logger) rely on this.
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Fatalf("want empty string for background ctx, got %q", got)
	}
}
