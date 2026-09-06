// Copyright (c) 2025 Reliant Labs
package codex

import (
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/logging"
)

// tokenRefreshTransport wraps another RoundTripper and transparently refreshes
// the OAuth access token when it is expired, mirroring the Claude driver's
// interceptor. On successful refresh (or adoption of tokens rotated by another
// goroutine/process) it rewrites the authorization header so subsequent
// requests use the new token. It also recovers from a 401 by reloading the
// persisted tokens once and retrying — covering the residual window where
// another process rotated the tokens after this request was sent.
//
// Codex differs from Claude in two ways that matter here:
//
//   - Expiry is not stored; it is the exp claim inside the JWT access token.
//     Refresh decisions therefore read the token itself via IsTokenExpired,
//     which already applies TokenRefreshBuffer for clock skew.
//   - Every request carries a chatgpt-account-id header bound to the account
//     in the access token. A rotation can in principle change it, so the
//     header is rewritten alongside the authorization header rather than
//     being captured once at construction.
type tokenRefreshTransport struct {
	base http.RoundTripper
	opts *llm.DriverOptions
	mu   *sync.RWMutex // guards opts token fields + headers

	// headers holds the per-request auth headers this transport owns
	// (authorization, chatgpt-account-id). Guarded by mu.
	headers map[string]string
}

func (t *tokenRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.refreshIfNeeded()

	t.applyHeaders(req)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// 401 resilience: another process may have rotated the OAuth tokens after
	// this driver loaded its credentials (refresh tokens are single-use, and
	// tokens from a superseded grant can be invalidated). Reload the persisted
	// tokens once; if they changed, retry the request once with the new token
	// instead of surfacing a terminal auth error.
	if resp.StatusCode == http.StatusUnauthorized && t.opts.TokenReloader != nil {
		stored, rerr := t.opts.TokenReloader()
		if rerr != nil {
			logging.Warn("Codex API returned 401 and reloading persisted tokens failed",
				"error", rerr, "user_id", t.opts.UserID)
			return resp, nil
		}
		if stored == nil || stored.AccessToken == "" {
			return resp, nil
		}
		t.mu.Lock()
		changed := stored.AccessToken != t.opts.ApiKey
		if changed {
			t.adoptLocked(*stored)
		}
		t.mu.Unlock()
		if !changed {
			// The store agrees with the token we just used. Before giving up,
			// try one real refresh: unlike Claude — whose expiry is persisted
			// and checked up front — a Codex token whose exp claim is
			// unparseable reaches here still looking "current" to the store.
			if t.refreshAfterUnauthorized() {
				changed = true
			}
		}
		if !changed {
			// A genuine auth failure, not a rotation race.
			return resp, nil
		}
		retryReq := cloneRequestForRetry(req)
		if retryReq == nil {
			// Body not replayable; the adopted token still fixes future requests.
			return resp, nil
		}
		logging.Warn("Codex API returned 401 but newer OAuth tokens are available; retrying once with rotated token",
			"user_id", t.opts.UserID)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		t.applyHeaders(retryReq)
		return t.base.RoundTrip(retryReq)
	}

	return resp, nil
}

// applyHeaders stamps the current auth headers onto a request. The SDK builds
// requests from options captured at construction, so the live token has to be
// written per request rather than baked in once.
func (t *tokenRefreshTransport) applyHeaders(req *http.Request) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for k, v := range t.headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}

// refreshIfNeeded refreshes the access token before a request when it is
// expired or within the expiry buffer. The heavy lifting (single-flight,
// cross-process coordination, persistence) lives in the TokenRefresher
// callback; this method just snapshots the held state and adopts the result.
func (t *tokenRefreshTransport) refreshIfNeeded() {
	if t.opts.TokenRefresher == nil {
		return
	}

	t.mu.RLock()
	held := llm.OAuthTokens{
		AccessToken:  t.opts.ApiKey,
		RefreshToken: t.opts.RefreshToken,
		ExpiresAt:    t.opts.TokenExpiresAt,
	}
	t.mu.RUnlock()

	if held.RefreshToken == "" {
		return
	}
	// Expiry comes from the token itself, so a zero/absent TokenExpiresAt is
	// not a reason to skip the check the way it is for Claude.
	if !IsTokenExpired(held.AccessToken) {
		return
	}

	newState, err := t.opts.TokenRefresher(held)
	if err != nil {
		logging.Warn("Failed to refresh Codex OAuth token", "error", err, "user_id", t.opts.UserID)
		// Continue with the existing token; the API will reject if truly expired
		return
	}

	t.mu.Lock()
	t.adoptLocked(newState)
	t.mu.Unlock()
	logging.Info("Codex OAuth token state updated", "new_expiry", newState.ExpiresAt, "user_id", t.opts.UserID)
}

// refreshAfterUnauthorized forces a refresh in response to a 401 that the
// store could not explain. It reports whether a genuinely different access
// token was adopted, which is the only case where retrying is worthwhile.
func (t *tokenRefreshTransport) refreshAfterUnauthorized() bool {
	if t.opts.TokenRefresher == nil {
		return false
	}

	t.mu.RLock()
	held := llm.OAuthTokens{
		AccessToken:  t.opts.ApiKey,
		RefreshToken: t.opts.RefreshToken,
		ExpiresAt:    t.opts.TokenExpiresAt,
	}
	t.mu.RUnlock()

	if held.RefreshToken == "" {
		return false
	}

	newState, err := t.opts.TokenRefresher(held)
	if err != nil {
		logging.Warn("Codex token refresh after 401 failed", "error", err, "user_id", t.opts.UserID)
		return false
	}
	if newState.AccessToken == "" || newState.AccessToken == held.AccessToken {
		return false
	}

	t.mu.Lock()
	t.adoptLocked(newState)
	t.mu.Unlock()
	return true
}

// adoptLocked installs a new token state on the options and rewrites the auth
// headers. Caller must hold t.mu.
func (t *tokenRefreshTransport) adoptLocked(state llm.OAuthTokens) {
	if state.AccessToken == "" {
		return
	}
	t.opts.ApiKey = state.AccessToken
	if state.RefreshToken != "" {
		t.opts.RefreshToken = state.RefreshToken
	}
	if state.ExpiresAt.IsZero() {
		if exp, err := GetTokenExpiry(state.AccessToken); err == nil {
			state.ExpiresAt = exp
		}
	}
	t.opts.TokenExpiresAt = state.ExpiresAt

	t.headers["authorization"] = "Bearer " + state.AccessToken
	// The account id is bound to the access token, so re-derive it rather than
	// carrying a stale value from the superseded token.
	if accountID, err := extractAccountIDFromJWT(state.AccessToken); err == nil && accountID != "" {
		t.headers["chatgpt-account-id"] = accountID
	}
}

// cloneRequestForRetry copies a request so it can be replayed. It returns nil
// when the body cannot be rewound, in which case the caller must not retry.
func cloneRequestForRetry(req *http.Request) *http.Request {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return clone
	}
	if req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil
	}
	clone.Body = body
	return clone
}

// tokenRefreshEnabled reports whether the driver options carry enough state to
// refresh transparently.
func tokenRefreshEnabled(opts llm.DriverOptions) bool {
	return opts.TokenRefresher != nil && opts.RefreshToken != ""
}

// codexTokenExpiryOrZero returns the JWT's expiry, or the zero time when it
// cannot be parsed.
func codexTokenExpiryOrZero(accessToken string) time.Time {
	exp, err := GetTokenExpiry(accessToken)
	if err != nil {
		return time.Time{}
	}
	return exp
}
