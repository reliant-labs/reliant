package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersMiddleware_Defaults(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	cfg := DefaultSecurityHeadersConfig()
	cfg.Enabled = true
	cfg.Production = false

	h := SecurityHeadersMiddleware(cfg)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	wantHeaders := map[string]string{
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=(), interest-cohort=(), payment=(), usb=()",
	}
	for k, v := range wantHeaders {
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("header %s: want %q got %q", k, v, got)
		}
	}
	// HSTS must NOT be emitted in non-production: emitting it from
	// localhost can pin a browser to https://localhost for 2 years.
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS must be unset in non-production, got %q", got)
	}
}

func TestSecurityHeadersMiddleware_ProductionEmitsHSTS(t *testing.T) {
	t.Parallel()

	cfg := DefaultSecurityHeadersConfig()
	cfg.Enabled = true
	cfg.Production = true

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := SecurityHeadersMiddleware(cfg)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := rec.Header().Get("Strict-Transport-Security")
	if got == "" {
		t.Fatal("HSTS must be set in production")
	}
	if !strings.Contains(got, "max-age=") {
		t.Fatalf("HSTS must contain max-age, got %q", got)
	}
	if !strings.Contains(got, "includeSubDomains") {
		t.Fatalf("HSTS must contain includeSubDomains, got %q", got)
	}
}

func TestSecurityHeadersMiddleware_NegativeHSTSDisables(t *testing.T) {
	t.Parallel()

	cfg := DefaultSecurityHeadersConfig()
	cfg.Enabled = true
	cfg.Production = true
	cfg.HSTSMaxAge = -1

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := SecurityHeadersMiddleware(cfg)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("negative HSTSMaxAge should disable HSTS, got %q", got)
	}
}

func TestSecurityHeadersMiddleware_DisabledIsPassthrough(t *testing.T) {
	t.Parallel()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	cfg := DefaultSecurityHeadersConfig()
	cfg.Enabled = false

	h := SecurityHeadersMiddleware(cfg)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("next must still run when middleware is disabled")
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("CSP must be unset when middleware is disabled, got %q", got)
	}
}

// The CSP for /debug/pprof/* is intentionally looser than the API default
// because pprof returns HTML index pages. Tests should assert both that
// pprof requests get the relaxed policy and that everything else keeps the
// strict one — mixing them up is how you break production navigations.
func TestSecurityHeadersMiddleware_PprofCSPIsRelaxed(t *testing.T) {
	t.Parallel()

	cfg := DefaultSecurityHeadersConfig()
	cfg.Enabled = true

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := SecurityHeadersMiddleware(cfg)(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/heap", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("pprof CSP should allow 'self', got %q", csp)
	}

	// Sibling non-pprof request must get the stricter policy.
	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/anything", http.NoBody)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	csp2 := rec2.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp2, "default-src 'none'") {
		t.Fatalf("non-pprof CSP should keep strict default, got %q", csp2)
	}
}
