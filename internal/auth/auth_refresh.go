// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrSessionExpired is returned when the stored access token is stale and cannot
// be refreshed — either no refresh_token is present, or the Supabase refresh
// endpoint rejected the refresh_token (revoked/expired). Callers should surface
// it as "session expired — run `reliant auth login`". Check with errors.Is.
var ErrSessionExpired = errors.New("session expired — run `reliant auth login`")

const (
	// refreshSkew is how close to expiry a token may be before we proactively
	// refresh it. A minute of headroom keeps an in-flight request from lapsing
	// between the local check and the server's own validation.
	refreshSkew = 60 * time.Second

	// refreshRequestTimeout bounds the non-interactive refresh HTTP round-trip
	// used by the context-less ReadAccessTokenFromAuthFile seam.
	refreshRequestTimeout = 15 * time.Second
)

// refreshMu serializes token refreshes within a single process so two
// goroutines cannot both spend the stored refresh_token and race to persist
// their (differently) rotated tokens, clobbering each other. After acquiring
// it we re-read the auth file and re-check staleness, so a goroutine that
// blocked while another refreshed simply reuses the fresh result instead of
// spending a second refresh. This is an in-process guard only; cross-process
// races (two `reliant` invocations refreshing at once) are left to Supabase's
// refresh-token reuse grace window — see the package notes / task report.
var refreshMu sync.Mutex

// EnsureFreshAccessToken returns a valid Supabase access token. If the stored
// token is still comfortably valid it is returned unchanged with no network
// I/O. If it is expired or within refreshSkew of expiry, EnsureFreshAccessToken
// exchanges the stored refresh_token for a new access_token (+ rotated
// refresh_token) at the Supabase token endpoint, persists both back to the auth
// file, and returns the fresh access token.
//
// Returns ("", nil) when no auth file exists — an unauthenticated machine.
// Returns an error wrapping ErrSessionExpired when the token is stale and the
// refresh cannot succeed (no refresh_token, or the endpoint returned 400/401).
// Transient failures (network, 5xx) return a plain error, NOT ErrSessionExpired,
// so a blip doesn't tell the user their session is gone. It never returns a
// stale token.
func EnsureFreshAccessToken(ctx context.Context) (string, error) {
	session, _, err := readPersistedAuthSession()
	if err != nil {
		return "", err
	}
	if session == nil {
		// No auth file at all — unauthenticated. Preserve legacy semantics so
		// callers keep their existing empty-string checks.
		return "", nil
	}

	token := strings.TrimSpace(session.AccessToken)
	if token != "" && !isTokenStale(token, time.Now()) {
		return token, nil
	}

	return refreshAndPersist(ctx, session)
}

// refreshAndPersist performs the refresh under the process-wide guard. session
// is the snapshot the caller read; after taking the lock we re-read the file so
// concurrent refreshes coalesce onto whichever tokens landed most recently.
func refreshAndPersist(ctx context.Context, session *persistedAuthSession) (string, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	// Double-check under the lock: another goroutine may have refreshed while we
	// waited. If so, reuse its result and its (newer) refresh_token.
	if current, _, err := readPersistedAuthSession(); err == nil && current != nil {
		if tok := strings.TrimSpace(current.AccessToken); tok != "" && !isTokenStale(tok, time.Now()) {
			return tok, nil
		}
		session = current
	}

	refreshToken := strings.TrimSpace(session.RefreshToken)
	if refreshToken == "" {
		return "", fmt.Errorf("%w (no refresh token stored)", ErrSessionExpired)
	}

	serverURL, anonKey, err := requireAuthConfig()
	if err != nil {
		return "", err
	}

	result, err := refreshAccessToken(ctx, serverURL, anonKey, refreshToken)
	if err != nil {
		return "", err
	}

	// Preserve identity fields if the refresh response omits them.
	userID := result.UserID
	if userID == "" {
		userID = session.User.ID
	}
	email := result.Email
	if email == "" {
		email = session.User.Email
	}

	if err := WriteAuthSession(result.AccessToken, result.RefreshToken, userID, email); err != nil {
		return "", fmt.Errorf("persisting refreshed auth session: %w", err)
	}

	return result.AccessToken, nil
}

// isTokenStale reports whether token should be refreshed at time now: it is
// expired, within refreshSkew of expiry, or cannot be decoded (in which case a
// refresh is the only path back to a usable token).
func isTokenStale(token string, now time.Time) bool {
	exp, err := decodeUnverifiedExp(token)
	if err != nil {
		return true
	}
	return now.Add(refreshSkew).After(exp)
}

// decodeUnverifiedExp extracts the `exp` claim from a JWT WITHOUT verifying its
// signature. We only need to know when the token expires to decide whether to
// refresh; the server enforces the signature. Mirrors the RawURLEncoding the
// JWTValidator uses for Supabase tokens.
func decodeUnverifiedExp(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("%w: malformed JWT", ErrInvalidToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: decoding payload", ErrInvalidToken)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("%w: parsing claims", ErrInvalidToken)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("%w: no exp claim", ErrInvalidToken)
	}
	return time.Unix(claims.Exp, 0), nil
}

// refreshAccessToken exchanges a refresh_token for a fresh token set at the
// Supabase token endpoint (grant_type=refresh_token). Mirrors the request shape
// of exchangeCodeForTokens. A 400/401 means the refresh_token is revoked or
// expired → ErrSessionExpired; other non-200s are treated as transient.
func refreshAccessToken(ctx context.Context, serverURL, anonKey, refreshToken string) (*LoginResult, error) {
	jsonBody, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, fmt.Errorf("marshaling refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/auth/v1/token?grant_type=refresh_token", strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refreshing access token: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading refresh response: %w", err)
	}

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w (refresh rejected: HTTP %d)", ErrSessionExpired, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tok tokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, fmt.Errorf("parsing refresh response: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return nil, fmt.Errorf("token refresh returned an empty access token")
	}

	return &LoginResult{
		AccessToken:   tok.AccessToken,
		RefreshToken:  tok.RefreshToken,
		UserID:        tok.User.ID,
		Email:         tok.User.Email,
		ProviderToken: tok.ProviderToken,
	}, nil
}
