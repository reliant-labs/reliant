package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		allowOrigins     []string
		allowCredentials bool
		requestOrigin    string
		method           string
		wantACAO         string // "" means header must NOT be set
		wantACAC         string
		wantStatus       int
		wantVary         bool
	}{
		{
			name:          "empty origin is left untouched",
			allowOrigins:  []string{"https://example.com"},
			requestOrigin: "",
			method:        http.MethodGet,
			wantStatus:    http.StatusOK,
			// Empty Origin: middleware must write no CORS headers at
			// all, including Vary: Origin. Same-origin requests and
			// non-browser clients should pass through transparently.
		},
		{
			name:          "exact origin match",
			allowOrigins:  []string{"https://app.example.com", "https://other.example.com"},
			requestOrigin: "https://app.example.com",
			method:        http.MethodGet,
			wantACAO:      "https://app.example.com",
			wantStatus:    http.StatusOK,
			wantVary:      true,
		},
		{
			name:          "origin miss emits no CORS headers",
			allowOrigins:  []string{"https://app.example.com"},
			requestOrigin: "https://evil.example",
			method:        http.MethodGet,
			wantStatus:    http.StatusOK,
			wantVary:      true,
		},
		{
			name:          "wildcard echoes origin (never writes literal *)",
			allowOrigins:  []string{"*"},
			requestOrigin: "https://any.example",
			method:        http.MethodGet,
			wantACAO:      "https://any.example",
			wantStatus:    http.StatusOK,
			wantVary:      true,
		},
		{
			name:             "exact origin + credentials",
			allowOrigins:     []string{"https://app.example.com"},
			allowCredentials: true,
			requestOrigin:    "https://app.example.com",
			method:           http.MethodGet,
			wantACAO:         "https://app.example.com",
			wantACAC:         "true",
			wantStatus:       http.StatusOK,
			wantVary:         true,
		},
		{
			name:          "preflight returns 204 with methods/headers",
			allowOrigins:  []string{"https://app.example.com"},
			requestOrigin: "https://app.example.com",
			method:        http.MethodOptions,
			wantACAO:      "https://app.example.com",
			wantStatus:    http.StatusNoContent,
			wantVary:      true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})
			h := CORSMiddleware(tc.allowOrigins, tc.allowCredentials)(next)

			req := httptest.NewRequestWithContext(t.Context(), tc.method, "/", http.NoBody)
			if tc.requestOrigin != "" {
				req.Header.Set("Origin", tc.requestOrigin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if got := rec.Result().StatusCode; got != tc.wantStatus {
				t.Fatalf("status: want %d got %d", tc.wantStatus, got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantACAO {
				t.Fatalf("ACAO: want %q got %q", tc.wantACAO, got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != tc.wantACAC {
				t.Fatalf("ACAC: want %q got %q", tc.wantACAC, got)
			}
			// Vary: Origin must appear whenever the response varies by
			// Origin. When no CORS headers are written (empty origin
			// pass-through), Vary must not be set — that was the H1 bug.
			if tc.wantVary {
				if got := rec.Header().Get("Vary"); got != "Origin" {
					t.Fatalf("Vary: want %q got %q", "Origin", got)
				}
			} else if got := rec.Header().Get("Vary"); got != "" {
				t.Fatalf("Vary: want unset, got %q", got)
			}
			// Preflight: middleware must short-circuit without
			// invoking next.
			if tc.method == http.MethodOptions && tc.wantACAO != "" {
				if nextCalled {
					t.Fatal("preflight must not call next")
				}
			} else if !nextCalled && tc.wantStatus == http.StatusOK {
				t.Fatal("non-preflight request should reach next")
			}
		})
	}
}

// TestCORSMiddleware_EmptyOrigin_NoHeaders is an explicit regression test for
// the H1 bug: a same-origin or non-browser request (no Origin header) must
// not cause the middleware to echo an empty ACAO or enable credentials.
func TestCORSMiddleware_EmptyOrigin_NoHeaders(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := CORSMiddleware([]string{"*"}, false)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Expose-Headers",
		"Vary",
	} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("with empty Origin, %s must be unset, got %q", header, got)
		}
	}
}

// TestCORSMiddleware_WildcardPlusCredentialsPanics verifies the belt-and-
// suspenders guard: even if config validation is bypassed, constructing
// the middleware with the spec-invalid "*"+credentials combination fails
// fast instead of silently emitting headers browsers reject.
func TestCORSMiddleware_WildcardPlusCredentialsPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("CORSMiddleware(*, allowCredentials=true) must panic")
		}
	}()
	_ = CORSMiddleware([]string{"*"}, true)
}

// Case-insensitive origin match is required because browsers lowercase the
// scheme+host but preserve case in some edge cases (IDN hosts, proxies).
// If an operator lists "https://App.example.com", a request from
// "https://app.example.com" must still be recognised.
func TestCORSMiddleware_CaseInsensitiveMatch(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := CORSMiddleware([]string{"https://APP.example.com"}, false)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Fatal("expected ACAO on case-insensitive match")
	}
}
