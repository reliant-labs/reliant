// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// stubIntrospector resolves a fixed set of tokens; everything else is
// inactive. down simulates an unreachable authority.
type stubIntrospector struct {
	principals map[string]*fat.Principal
	down       bool
	calls      int
}

func (s *stubIntrospector) Introspect(_ context.Context, token string) (*fat.Principal, error) {
	s.calls++
	if s.down {
		return nil, errors.New("dial tcp: connection refused")
	}
	if p, ok := s.principals[token]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("introspect: %w", ErrAccessTokenInactive)
}

func mustMint(t *testing.T) string {
	t.Helper()
	m, err := fat.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	return m.Plaintext
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
		_, machine := MachineTokenFromContext(r.Context())
		fmt.Fprintf(w, "%s machine=%t", userID, machine)
	})
}

func doGet(t *testing.T, url, bearer string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

// TestRequireAuthOrAccessToken proves the HTTP side of the shape dispatch:
// `rlat_` bearers resolve through the authority to the same identity context a
// session produces, reliant:api only; the session path is untouched; and an
// unreachable authority is 503, never a 401 rejection.
func TestRequireAuthOrAccessToken(t *testing.T) {
	mw := newAPIKeyMiddleware(t)
	apiToken, daemonToken := mustMint(t), mustMint(t)
	stub := &stubIntrospector{principals: map[string]*fat.Principal{
		apiToken:    {TokenID: "t1", ActingUserID: "user-from-token", Scopes: fat.SetOf(fat.ScopeReliantAPI)},
		daemonToken: {TokenID: "t2", ActingUserID: "user-from-token", Scopes: fat.SetOf(fat.ScopeDaemonConnect)},
	}}
	srv := httptest.NewServer(mw.RequireAuthOrAccessToken(stub)(echoUserHandler(t)))
	t.Cleanup(srv.Close)

	if status, body := doGet(t, srv.URL, apiToken); status != http.StatusOK || body != "user-from-token machine=true" {
		t.Errorf("reliant:api bearer = (%d, %q), want (200, user-from-token machine=true)", status, body)
	}
	if stub.calls == 0 {
		t.Error("introspector was never consulted")
	}
	if status, _ := doGet(t, srv.URL, daemonToken); status != http.StatusUnauthorized {
		t.Errorf("daemon:connect bearer status = %d, want 401", status)
	}
	if status, _ := doGet(t, srv.URL, mustMint(t)); status != http.StatusUnauthorized {
		t.Errorf("unknown access token status = %d, want 401", status)
	}
	if status, body := doGet(t, srv.URL, "legacy-secret"); status != http.StatusOK || body == "" || body[len(body)-5:] != "false" {
		t.Errorf("apikey bearer = (%d, %q), want 200 non-machine", status, body)
	}
	if status, _ := doGet(t, srv.URL, ""); status != http.StatusUnauthorized {
		t.Errorf("missing bearer status = %d, want 401", status)
	}

	stub.down = true
	if status, _ := doGet(t, srv.URL, apiToken); status != http.StatusServiceUnavailable {
		t.Errorf("authority unreachable status = %d, want 503 (not a rejection)", status)
	}
}

// TestRequireAuthRejectsAccessTokens: the session-only middleware rejects
// `rlat_` bearers outright rather than feeding them to the JWT validator.
func TestRequireAuthRejectsAccessTokens(t *testing.T) {
	mw := newAPIKeyMiddleware(t)
	srv := httptest.NewServer(mw.RequireAuth(echoUserHandler(t)))
	t.Cleanup(srv.Close)
	if status, _ := doGet(t, srv.URL, mustMint(t)); status != http.StatusUnauthorized {
		t.Errorf("access token on session-only middleware = %d, want 401", status)
	}
}
