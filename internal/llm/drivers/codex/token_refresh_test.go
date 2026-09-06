// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
)

func mintJWT(t *testing.T, accountID string, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"exp": expiry.Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
	require.NoError(t, err)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// recordingTransport captures the authorization header of every request it
// sees and returns a scripted status per call.
type recordingTransport struct {
	mu       sync.Mutex
	authSeen []string
	acctSeen []string
	statuses []int
	calls    int
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.authSeen = append(rt.authSeen, req.Header.Get("authorization"))
	rt.acctSeen = append(rt.acctSeen, req.Header.Get("chatgpt-account-id"))

	status := http.StatusOK
	if rt.calls < len(rt.statuses) {
		status = rt.statuses[rt.calls]
	}
	rt.calls++
	return &http.Response{
		StatusCode: status,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func newTestTransport(t *testing.T, base http.RoundTripper, opts *llm.DriverOptions, accessToken, accountID string) *tokenRefreshTransport {
	t.Helper()
	return &tokenRefreshTransport{
		base: base,
		opts: opts,
		mu:   &sync.RWMutex{},
		headers: map[string]string{
			"authorization":      "Bearer " + accessToken,
			"chatgpt-account-id": accountID,
		},
	}
}

// TestTransport_RefreshesExpiredTokenBeforeRequest is the regression test for
// the reported bug: a Codex access token that has already expired was sent
// upstream unchanged, producing `401 token_expired` and failing the workflow,
// even though a usable refresh token was sitting in the database.
func TestTransport_RefreshesExpiredTokenBeforeRequest(t *testing.T) {
	expired := mintJWT(t, "acct-1", time.Now().Add(-time.Hour))
	fresh := mintJWT(t, "acct-1", time.Now().Add(8*time.Hour))

	var refreshCalls int
	opts := &llm.DriverOptions{
		ApiKey:       expired,
		RefreshToken: "rt-1",
		TokenRefresher: func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
			refreshCalls++
			assert.Equal(t, "rt-1", held.RefreshToken)
			return llm.OAuthTokens{AccessToken: fresh, RefreshToken: "rt-2"}, nil
		},
	}

	base := &recordingTransport{}
	transport := newTestTransport(t, base, opts, expired, "acct-1")

	req, err := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, 1, refreshCalls, "expired token must trigger exactly one refresh")
	require.Len(t, base.authSeen, 1)
	assert.Equal(t, "Bearer "+fresh, base.authSeen[0],
		"the request must carry the REFRESHED token, not the expired one")
	assert.NotContains(t, base.authSeen[0], expired)
}

// TestTransport_LiveTokenIsNotRefreshed guards the other direction: a healthy
// token must not burn a single-use refresh token on every request.
func TestTransport_LiveTokenIsNotRefreshed(t *testing.T) {
	live := mintJWT(t, "acct-1", time.Now().Add(8*time.Hour))

	refreshCalls := 0
	opts := &llm.DriverOptions{
		ApiKey:       live,
		RefreshToken: "rt-1",
		TokenRefresher: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
			refreshCalls++
			return llm.OAuthTokens{}, nil
		},
	}

	base := &recordingTransport{}
	transport := newTestTransport(t, base, opts, live, "acct-1")

	req, err := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	assert.Zero(t, refreshCalls, "a live token must not trigger a refresh")
	require.Len(t, base.authSeen, 1)
	assert.Equal(t, "Bearer "+live, base.authSeen[0])
}

// TestTransport_RetriesOnceAfter401FromConcurrentRotation covers the residual
// window: the token looked live when the request was sent, but another process
// had already rotated it. The store's newer token is adopted and the request
// is retried once rather than failing the workflow.
func TestTransport_RetriesOnceAfter401FromConcurrentRotation(t *testing.T) {
	live := mintJWT(t, "acct-1", time.Now().Add(8*time.Hour))
	// Distinct expiry so the two tokens are distinguishable strings: identical
	// claims minted in the same second produce byte-identical JWTs, which
	// would make "rotated" indistinguishable from "live".
	rotated := mintJWT(t, "acct-1", time.Now().Add(9*time.Hour))

	opts := &llm.DriverOptions{
		ApiKey:       live,
		RefreshToken: "rt-1",
		TokenRefresher: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
			t.Error("reload should satisfy this case without an upstream refresh")
			return llm.OAuthTokens{}, nil
		},
		TokenReloader: func() (*llm.OAuthTokens, error) {
			return &llm.OAuthTokens{AccessToken: rotated, RefreshToken: "rt-2"}, nil
		},
	}

	base := &recordingTransport{statuses: []int{http.StatusUnauthorized, http.StatusOK}}
	transport := newTestTransport(t, base, opts, live, "acct-1")

	req, err := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode, "the retry must succeed")
	require.Len(t, base.authSeen, 2, "exactly one retry")
	assert.Equal(t, "Bearer "+live, base.authSeen[0])
	assert.Equal(t, "Bearer "+rotated, base.authSeen[1], "retry must use the rotated token")
}

// TestTransport_GenuineAuthFailureIsNotRetried: when the store agrees with the
// token we used and a forced refresh yields nothing new, the 401 is real and
// must surface rather than spinning.
func TestTransport_GenuineAuthFailureIsNotRetried(t *testing.T) {
	live := mintJWT(t, "acct-1", time.Now().Add(8*time.Hour))

	opts := &llm.DriverOptions{
		ApiKey:       live,
		RefreshToken: "rt-1",
		TokenRefresher: func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
			// Refresh yields the same token: nothing to retry with.
			return llm.OAuthTokens{AccessToken: held.AccessToken, RefreshToken: held.RefreshToken}, nil
		},
		TokenReloader: func() (*llm.OAuthTokens, error) {
			return &llm.OAuthTokens{AccessToken: live, RefreshToken: "rt-1"}, nil
		},
	}

	base := &recordingTransport{statuses: []int{http.StatusUnauthorized, http.StatusOK}}
	transport := newTestTransport(t, base, opts, live, "acct-1")

	req, err := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a real 401 must surface")
	assert.Len(t, base.authSeen, 1, "no retry when there is no newer token")
}

// TestTransport_AdoptRewritesAccountHeader pins that the account id travels
// with the token. Every Codex request carries chatgpt-account-id bound to the
// access token, so a rotation that changed accounts would otherwise send a
// fresh token under a stale account.
func TestTransport_AdoptRewritesAccountHeader(t *testing.T) {
	expired := mintJWT(t, "acct-old", time.Now().Add(-time.Hour))
	fresh := mintJWT(t, "acct-new", time.Now().Add(8*time.Hour))

	opts := &llm.DriverOptions{
		ApiKey:       expired,
		RefreshToken: "rt-1",
		TokenRefresher: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
			return llm.OAuthTokens{AccessToken: fresh, RefreshToken: "rt-2"}, nil
		},
	}

	base := &recordingTransport{}
	transport := newTestTransport(t, base, opts, expired, "acct-old")

	req, err := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	require.Len(t, base.acctSeen, 1)
	assert.Equal(t, "acct-new", base.acctSeen[0],
		"the account header must be re-derived from the refreshed token")
}

// TestTransport_UnparseableTokenStillRefreshes: a token whose exp claim cannot
// be read is treated as expired (IsTokenExpired's own disposition), so the
// refresh path still runs instead of sending a token we cannot reason about.
func TestTransport_UnparseableTokenStillRefreshes(t *testing.T) {
	fresh := mintJWT(t, "acct-1", time.Now().Add(8*time.Hour))

	refreshCalls := 0
	opts := &llm.DriverOptions{
		ApiKey:       "not-a-jwt",
		RefreshToken: "rt-1",
		TokenRefresher: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
			refreshCalls++
			return llm.OAuthTokens{AccessToken: fresh, RefreshToken: "rt-2"}, nil
		},
	}

	base := &recordingTransport{}
	transport := newTestTransport(t, base, opts, "not-a-jwt", "acct-1")

	req, err := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, 1, refreshCalls)
	require.Len(t, base.authSeen, 1)
	assert.True(t, strings.HasSuffix(base.authSeen[0], fresh))
}
