// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"io"
	"net/http"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/logging"
)

// NewTokenRefreshTransport wraps base so every outbound request carries a live
// Google access token. The driver (owned separately) installs this on its HTTP
// client; it is exported because the driver lives in the same package but is
// written by a different change, and a constructor is a narrower contract than
// a bare struct.
//
// Returns base unchanged when the options carry no refresher, so an
// API-key-style configuration is unaffected.
func NewTokenRefreshTransport(base http.RoundTripper, opts *llm.DriverOptions) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if opts == nil || opts.TokenRefresher == nil || opts.RefreshToken == "" {
		return base
	}
	return &tokenRefreshTransport{
		base: base,
		opts: opts,
		mu:   &sync.RWMutex{},
		headers: map[string]string{
			"authorization": "Bearer " + opts.ApiKey,
		},
	}
}

// tokenRefreshTransport transparently refreshes the OAuth access token when it
// is expired, and recovers from a 401 by reloading persisted tokens once and
// retrying. It mirrors the Codex transport, with one difference that matters:
//
//	Antigravity's access token is an opaque `ya29.` Google token, NOT a JWT.
//	There is no exp claim to read out of it, so refresh decisions use the
//	STORED expiry (opts.TokenExpiresAt) the way Claude's do — never the token
//	contents. Copying Codex's IsTokenExpired(accessToken) here would compile
//	against a string and always report "expired".
type tokenRefreshTransport struct {
	base http.RoundTripper
	opts *llm.DriverOptions
	mu   *sync.RWMutex // guards opts token fields + headers

	// headers holds the auth headers this transport owns. Guarded by mu.
	headers map[string]string
}

func (t *tokenRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.refreshIfNeeded()

	t.applyHeaders(req)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// 401 resilience: another process may have rotated the tokens after this
	// driver loaded its credentials. Reload the persisted tokens once; if they
	// changed, retry rather than surfacing a terminal auth error.
	if resp.StatusCode == http.StatusUnauthorized && t.opts.TokenReloader != nil {
		stored, rerr := t.opts.TokenReloader()
		if rerr != nil {
			logging.Warn("Antigravity API returned 401 and reloading persisted tokens failed",
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
			// The store agrees with the token we just used. A 401 on a token
			// the store still considers current means the stored expiry is
			// wrong (clock skew, or a token revoked upstream), so force one
			// real refresh before giving up.
			changed = t.refreshAfterUnauthorized()
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
		logging.Warn("Antigravity API returned 401 but newer OAuth tokens are available; retrying once",
			"user_id", t.opts.UserID)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		t.applyHeaders(retryReq)
		return t.base.RoundTrip(retryReq)
	}

	return resp, nil
}

// applyHeaders stamps the current auth headers onto a request. The live token
// has to be written per request rather than baked in at construction.
func (t *tokenRefreshTransport) applyHeaders(req *http.Request) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for k, v := range t.headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}

// refreshIfNeeded refreshes the access token before a request when the stored
// expiry says it is expired or within the buffer. The single-flight,
// cross-process coordination and persistence all live in the TokenRefresher
// callback; this only snapshots the held state and adopts the result.
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
	if !IsTokenExpired(held.ExpiresAt) {
		return
	}

	newState, err := t.opts.TokenRefresher(held)
	if err != nil {
		logging.Warn("Failed to refresh Antigravity OAuth token", "error", err, "user_id", t.opts.UserID)
		// Continue with the existing token; the API will reject if truly expired.
		return
	}

	t.mu.Lock()
	t.adoptLocked(newState)
	t.mu.Unlock()
	logging.Info("Antigravity OAuth token state updated", "new_expiry", newState.ExpiresAt, "user_id", t.opts.UserID)
}

// refreshAfterUnauthorized forces a refresh in response to a 401 the store
// could not explain. It reports whether a genuinely different access token was
// adopted, which is the only case where retrying is worthwhile.
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
		logging.Warn("Antigravity token refresh after 401 failed", "error", err, "user_id", t.opts.UserID)
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
// header. Caller must hold t.mu.
func (t *tokenRefreshTransport) adoptLocked(state llm.OAuthTokens) {
	if state.AccessToken == "" {
		return
	}
	t.opts.ApiKey = state.AccessToken
	// Google does not rotate the refresh token on refresh, so an empty value
	// here means "unchanged", not "revoked".
	if state.RefreshToken != "" {
		t.opts.RefreshToken = state.RefreshToken
	}
	t.opts.TokenExpiresAt = state.ExpiresAt
	t.headers["authorization"] = "Bearer " + state.AccessToken
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
