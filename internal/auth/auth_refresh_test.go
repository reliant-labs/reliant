package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// makeJWT mints an unsigned-but-well-formed JWT whose payload carries the given
// exp. The refresh path only decodes exp (it never verifies the signature), so
// a placeholder signature segment is sufficient for these tests.
func makeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`))
	payloadJSON, err := json.Marshal(map[string]any{"exp": exp.Unix(), "sub": "user-123"})
	if err != nil {
		t.Fatalf("marshaling payload: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sig := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return header + "." + payload + "." + sig
}

// withTempAuthHome points the auth-file path resolver at an isolated temp HOME
// so tests never read or clobber the developer's real credentials.
func withTempAuthHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
}

// readAuthFileRaw reads the persisted auth file back for assertions.
func readAuthFileRaw(t *testing.T) writeAuthSession {
	t.Helper()
	path, err := CurrentAuthFilePath()
	if err != nil {
		t.Fatalf("CurrentAuthFilePath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading auth file: %v", err)
	}
	var s writeAuthSession
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshaling auth file: %v", err)
	}
	return s
}

func TestIsTokenStale(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tests := []struct {
		name      string
		token     string
		wantStale bool
	}{
		{"far future", makeJWT(t, now.Add(10*time.Minute)), false},
		{"comfortably valid", makeJWT(t, now.Add(2*time.Minute)), false},
		{"within skew (<60s)", makeJWT(t, now.Add(30*time.Second)), true},
		{"exactly expired", makeJWT(t, now), true},
		{"long expired", makeJWT(t, now.Add(-10*time.Minute)), true},
		{"undecodable", "not-a-jwt", true},
		{"empty", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTokenStale(tc.token, now); got != tc.wantStale {
				t.Fatalf("isTokenStale(%q) = %v, want %v", tc.name, got, tc.wantStale)
			}
		})
	}
}

func TestDecodeUnverifiedExp(t *testing.T) {
	want := time.Unix(1_700_000_000, 0)
	tok := makeJWT(t, want)
	got, err := decodeUnverifiedExp(tok)
	if err != nil {
		t.Fatalf("decodeUnverifiedExp: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("exp = %v, want %v", got, want)
	}

	if _, err := decodeUnverifiedExp("only.two"); err == nil {
		t.Fatal("expected error for malformed JWT")
	}
}

// refreshHandler builds a Supabase-shaped refresh endpoint that returns a fresh
// token set and records the request it saw. status controls the HTTP status.
func refreshHandler(t *testing.T, status int, newAccess, newRefresh string, seen *struct {
	apikey       string
	grant        string
	refreshToken string
	hits         int
}) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		seen.hits++
		seen.apikey = r.Header.Get("apikey")
		seen.grant = r.URL.Query().Get("grant_type")
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		seen.refreshToken = req["refresh_token"]

		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"refresh rejected"}`)
			return
		}
		resp := map[string]any{
			"access_token":  newAccess,
			"refresh_token": newRefresh,
			"token_type":    "bearer",
			"expires_in":    3600,
			"user":          map[string]string{"id": "user-123", "email": "u@example.com"},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func TestEnsureFreshAccessToken_FreshTokenNoNetwork(t *testing.T) {
	withTempAuthHome(t)
	fresh := makeJWT(t, time.Now().Add(30*time.Minute))
	if err := WriteAuthSession(fresh, "refresh-abc", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}

	// Point the endpoint at a server that fails the test if it is ever hit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refresh endpoint hit for a fresh token")
	}))
	defer srv.Close()
	withCompiledAuthDefaults(t, srv.URL, "anon-key")
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")

	got, err := EnsureFreshAccessToken(context.Background())
	if err != nil {
		t.Fatalf("EnsureFreshAccessToken: %v", err)
	}
	if got != fresh {
		t.Fatalf("returned token changed for a fresh token")
	}
}

func TestEnsureFreshAccessToken_RefreshesAndPersists(t *testing.T) {
	withTempAuthHome(t)
	stale := makeJWT(t, time.Now().Add(-time.Minute))
	if err := WriteAuthSession(stale, "old-refresh", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}

	newAccess := makeJWT(t, time.Now().Add(time.Hour))
	var seen struct {
		apikey       string
		grant        string
		refreshToken string
		hits         int
	}
	srv := httptest.NewServer(refreshHandler(t, http.StatusOK, newAccess, "new-refresh", &seen))
	defer srv.Close()
	withCompiledAuthDefaults(t, srv.URL, "anon-key")
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")

	got, err := EnsureFreshAccessToken(context.Background())
	if err != nil {
		t.Fatalf("EnsureFreshAccessToken: %v", err)
	}

	// Returned the fresh token.
	if got != newAccess {
		t.Fatalf("returned token is not the refreshed one")
	}
	// Sent the right request.
	if seen.hits != 1 {
		t.Fatalf("expected exactly 1 refresh call, got %d", seen.hits)
	}
	if seen.apikey != "anon-key" {
		t.Fatalf("apikey header = %q, want %q", seen.apikey, "anon-key")
	}
	if seen.grant != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", seen.grant)
	}
	if seen.refreshToken != "old-refresh" {
		t.Fatalf("sent refresh_token = %q, want old-refresh", seen.refreshToken)
	}
	// Persisted the rotated tokens.
	persisted := readAuthFileRaw(t)
	if persisted.AccessToken != newAccess {
		t.Fatalf("persisted access token was not updated")
	}
	if persisted.RefreshToken != "new-refresh" {
		t.Fatalf("persisted refresh_token = %q, want new-refresh (rotation not saved)", persisted.RefreshToken)
	}
	if persisted.User.ID != "user-123" || persisted.User.Email != "u@example.com" {
		t.Fatalf("identity fields not preserved: %+v", persisted.User)
	}
}

func TestEnsureFreshAccessToken_NoRefreshToken(t *testing.T) {
	withTempAuthHome(t)
	stale := makeJWT(t, time.Now().Add(-time.Minute))
	if err := WriteAuthSession(stale, "", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}
	withCompiledAuthDefaults(t, "http://unused.localhost", "anon-key")
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")

	_, err := EnsureFreshAccessToken(context.Background())
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
	// Stale token must NOT have been silently returned, and the file untouched.
	if got := readAuthFileRaw(t).AccessToken; got != stale {
		t.Fatalf("auth file mutated on a no-refresh-token failure")
	}
}

func TestEnsureFreshAccessToken_RefreshRejected(t *testing.T) {
	withTempAuthHome(t)
	stale := makeJWT(t, time.Now().Add(-time.Minute))
	if err := WriteAuthSession(stale, "revoked-refresh", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}
	var seen struct {
		apikey       string
		grant        string
		refreshToken string
		hits         int
	}
	srv := httptest.NewServer(refreshHandler(t, http.StatusBadRequest, "", "", &seen))
	defer srv.Close()
	withCompiledAuthDefaults(t, srv.URL, "anon-key")
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")

	_, err := EnsureFreshAccessToken(context.Background())
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired on 400", err)
	}
	if got := readAuthFileRaw(t).AccessToken; got != stale {
		t.Fatalf("auth file mutated on a rejected refresh")
	}
}

func TestEnsureFreshAccessToken_TransientErrorNotSessionExpired(t *testing.T) {
	withTempAuthHome(t)
	stale := makeJWT(t, time.Now().Add(-time.Minute))
	if err := WriteAuthSession(stale, "some-refresh", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}
	var seen struct {
		apikey       string
		grant        string
		refreshToken string
		hits         int
	}
	srv := httptest.NewServer(refreshHandler(t, http.StatusInternalServerError, "", "", &seen))
	defer srv.Close()
	withCompiledAuthDefaults(t, srv.URL, "anon-key")
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")

	_, err := EnsureFreshAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected an error on HTTP 500")
	}
	if errors.Is(err, ErrSessionExpired) {
		t.Fatal("a 5xx must NOT be reported as ErrSessionExpired (it is transient)")
	}
}

func TestEnsureFreshAccessToken_NoAuthFile(t *testing.T) {
	withTempAuthHome(t) // empty HOME, no auth file written
	got, err := EnsureFreshAccessToken(context.Background())
	if err != nil {
		t.Fatalf("expected nil error for missing auth file, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty token for missing auth file, got non-empty")
	}
}

func TestReadAccessTokenFromAuthFile_DelegatesRefresh(t *testing.T) {
	withTempAuthHome(t)
	stale := makeJWT(t, time.Now().Add(-time.Minute))
	if err := WriteAuthSession(stale, "old-refresh", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}
	newAccess := makeJWT(t, time.Now().Add(time.Hour))
	var seen struct {
		apikey       string
		grant        string
		refreshToken string
		hits         int
	}
	srv := httptest.NewServer(refreshHandler(t, http.StatusOK, newAccess, "new-refresh", &seen))
	defer srv.Close()
	withCompiledAuthDefaults(t, srv.URL, "anon-key")
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")

	got, err := ReadAccessTokenFromAuthFile()
	if err != nil {
		t.Fatalf("ReadAccessTokenFromAuthFile: %v", err)
	}
	if got != newAccess {
		t.Fatalf("legacy accessor did not return the refreshed token")
	}
	if seen.hits != 1 {
		t.Fatalf("expected the legacy accessor to trigger exactly 1 refresh, got %d", seen.hits)
	}
}

func TestPeekAccessTokenFromAuthFile_NoRefresh(t *testing.T) {
	withTempAuthHome(t)
	stale := makeJWT(t, time.Now().Add(-time.Minute))
	if err := WriteAuthSession(stale, "old-refresh", "user-123", "u@example.com"); err != nil {
		t.Fatalf("WriteAuthSession: %v", err)
	}
	// Endpoint fails the test if hit — Peek must never refresh.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Peek must not hit the refresh endpoint")
	}))
	defer srv.Close()
	withCompiledAuthDefaults(t, srv.URL, "anon-key")

	got, err := PeekAccessTokenFromAuthFile()
	if err != nil {
		t.Fatalf("PeekAccessTokenFromAuthFile: %v", err)
	}
	if got != stale {
		t.Fatalf("Peek returned a different token than what is stored")
	}
	if strings.TrimSpace(readAuthFileRaw(t).AccessToken) != stale {
		t.Fatalf("Peek mutated the auth file")
	}
}
