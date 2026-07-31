// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubAPITokenValidator accepts exactly one raw token.
type stubAPITokenValidator struct {
	accept string
	claims *JWTClaims
	calls  int
}

func (s *stubAPITokenValidator) ValidateAPIToken(_ context.Context, rawToken string) (*JWTClaims, error) {
	s.calls++
	if rawToken == s.accept {
		return s.claims, nil
	}
	return nil, fmt.Errorf("invalid or expired token")
}

func newAPIKeyMiddleware(t *testing.T) *Middleware {
	t.Helper()
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-secret")
	mw, err := NewMiddleware("", "")
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}
	return mw
}

func echoUserHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, _ := GetUserIDFromContext(r.Context())
		fmt.Fprint(w, userID)
	})
}

// TestRequireAuthOrAPIToken proves the HTTP middleware side of the prefix
// dispatch: PAT-format bearers resolve through the api-token validator to the
// same identity context the JWT/apikey path produces, and the JWT/apikey path
// is untouched.
func TestRequireAuthOrAPIToken(t *testing.T) {
	mw := newAPIKeyMiddleware(t)
	patToken := PATPrefix + "abcdefghijklmnopqrstuvwxyz0123456789"
	stub := &stubAPITokenValidator{
		accept: patToken,
		claims: &JWTClaims{Sub: "user-from-pat", Email: "p@example.com", Role: "authenticated"},
	}
	srv := httptest.NewServer(mw.RequireAuthOrAPIToken(stub)(echoUserHandler(t)))
	t.Cleanup(srv.Close)

	get := func(bearer string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// PAT bearer resolves through the validator.
	if status, body := get(patToken); status != http.StatusOK || body != "user-from-pat" {
		t.Errorf("PAT bearer = (%d, %q), want (200, user-from-pat)", status, body)
	}
	if stub.calls == 0 {
		t.Error("PAT validator was never consulted")
	}

	// Unknown PATs are rejected without falling through to the JWT path.
	if status, _ := get(PATPrefix + "0000000000000000000000000000000000000000"); status != http.StatusUnauthorized {
		t.Errorf("unknown PAT status = %d, want 401", status)
	}

	// The legacy (apikey-mode) bearer path is untouched.
	if status, _ := get("legacy-secret"); status != http.StatusOK {
		t.Errorf("apikey bearer status = %d, want 200", status)
	}

	// Missing bearer still 401s.
	if status, _ := get(""); status != http.StatusUnauthorized {
		t.Errorf("missing bearer status = %d, want 401", status)
	}
}

// TestRequireAuthRejectsPATs: the JWT-only middleware (token management
// endpoints) rejects PAT bearers outright — a PAT cannot manage PATs.
func TestRequireAuthRejectsPATs(t *testing.T) {
	mw := newAPIKeyMiddleware(t)
	srv := httptest.NewServer(mw.RequireAuth(echoUserHandler(t)))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+PATPrefix+"abcdefghijklmnopqrstuvwxyz0123456789")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("PAT on JWT-only middleware = %d, want 401", resp.StatusCode)
	}
}
