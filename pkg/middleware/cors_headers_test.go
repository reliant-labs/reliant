package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func preflight(t *testing.T, h http.Handler) http.Header {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, "/svc/Method", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Header()
}

// A web client header that is not on forge's built-in list made the browser
// refuse every RPC, and a project could not add it: forge answers the
// preflight itself, before the project's middleware runs. (2026-10-07:
// reliant's web client stopped sending x-daemon-last-seen to control-plane to
// work around it.) The project now declares its headers, on top of the
// defaults, in production and in dev alike.
func TestCORS_ProjectDeclaredHeadersExtendTheDefaults(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for name, mw := range map[string]func(http.Handler) http.Handler{
		"production": CORSMiddleware([]string{"https://app.example.com"}, true,
			WithAllowHeaders(HeaderList("x-daemon-last-seen, X-Request-Id")...),
			WithExposeHeaders("x-daemon-generation")),
		"dev": DevCORSMiddleware(true,
			WithAllowHeaders("x-daemon-last-seen"), WithExposeHeaders("x-daemon-generation")),
	} {
		got := preflight(t, mw(next))
		allow := got.Get("Access-Control-Allow-Headers")
		for _, want := range append([]string{"x-daemon-last-seen"}, DefaultCORSAllowHeaders...) {
			if !strings.Contains(allow, want) {
				t.Errorf("%s: Allow-Headers %q lacks %q", name, allow, want)
			}
		}
		if strings.Count(strings.ToLower(allow), "x-request-id") != 1 {
			t.Errorf("%s: a header declared twice must be listed once: %q", name, allow)
		}
		expose := got.Get("Access-Control-Expose-Headers")
		for _, want := range append([]string{"x-daemon-generation"}, DefaultCORSExposeHeaders...) {
			if !strings.Contains(expose, want) {
				t.Errorf("%s: Expose-Headers %q lacks %q", name, expose, want)
			}
		}
	}
}

// With no options, the policy is exactly forge's defaults (Forge-Via joined
// them with the activity stream; x-forge-error-scopes, with scope_missing).
func TestCORS_DefaultsAreUnchanged(t *testing.T) {
	got := preflight(t, CORSMiddleware([]string{"https://app.example.com"}, false)(http.NotFoundHandler()))
	if want := "Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms, Authorization, Traceparent, Tracestate, X-Request-Id, Forge-Via"; got.Get("Access-Control-Allow-Headers") != want {
		t.Errorf("Allow-Headers = %q, want %q", got.Get("Access-Control-Allow-Headers"), want)
	}
	if want := "Connect-Protocol-Version, x-forge-error-reason, x-forge-error-scopes, Traceresponse"; got.Get("Access-Control-Expose-Headers") != want {
		t.Errorf("Expose-Headers = %q, want %q", got.Get("Access-Control-Expose-Headers"), want)
	}
}
