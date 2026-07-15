// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
)

// capturedRequest records what the fake upstream saw for one round trip.
type capturedRequest struct {
	authorization string
	body          string
}

// scriptedRoundTripper returns the scripted status codes in order and captures
// each request's authorization header and body.
type scriptedRoundTripper struct {
	mu       sync.Mutex
	statuses []int
	requests []capturedRequest
}

func (s *scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var body string
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		body = string(b)
	}

	auth := ""
	if vals, ok := req.Header["authorization"]; ok && len(vals) > 0 {
		auth = vals[0]
	}
	s.requests = append(s.requests, capturedRequest{authorization: auth, body: body})

	if len(s.requests) > len(s.statuses) {
		return nil, fmt.Errorf("scriptedRoundTripper: unexpected request %d", len(s.requests))
	}
	status := s.statuses[len(s.requests)-1]
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error"}`)),
		Request:    req,
	}, nil
}

// newTestTokenRefreshTransport builds the same transport chain
// NewClaudeCodeClient builds (tokenRefreshTransport -> lowercaseHeaderTransport
// -> upstream) around a scripted upstream.
func newTestTokenRefreshTransport(opts *llm.DriverOptions, upstream http.RoundTripper) *tokenRefreshTransport {
	headers := map[string]string{
		"authorization": "Bearer " + opts.ApiKey,
	}
	mu := &sync.RWMutex{}
	return &tokenRefreshTransport{
		base:             &lowercaseHeaderTransport{base: upstream, headers: headers, mu: mu},
		opts:             opts,
		lowercaseHeaders: headers,
		mu:               mu,
	}
}

func newMessagesRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude-fable-5"}`))
	require.NoError(t, err)
	return req
}

// TestTokenRefreshTransport_401ReloadsStoreAndRetriesOnce: another process
// rotated the tokens after this driver loaded its credentials, so the request
// 401s. The transport must reload the persisted tokens once and silently retry
// with the rotated token instead of surfacing a terminal auth error.
func TestTokenRefreshTransport_401ReloadsStoreAndRetriesOnce(t *testing.T) {
	upstream := &scriptedRoundTripper{statuses: []int{http.StatusUnauthorized, http.StatusOK}}

	reloads := 0
	opts := &llm.DriverOptions{
		ApiKey:         "stale-token",
		UserID:         "user-1",
		RefreshToken:   "rt-1",
		TokenExpiresAt: time.Now().Add(time.Hour), // not expired: no pre-flight refresh
		TokenReloader: func() (*llm.OAuthTokens, error) {
			reloads++
			return &llm.OAuthTokens{
				AccessToken:  "rotated-token",
				RefreshToken: "rt-2",
				ExpiresAt:    time.Now().Add(8 * time.Hour),
			}, nil
		},
	}
	transport := newTestTokenRefreshTransport(opts, upstream)

	resp, err := transport.RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the retry must succeed silently")
	assert.Equal(t, 1, reloads, "store must be reloaded exactly once")

	require.Len(t, upstream.requests, 2)
	assert.Equal(t, "Bearer stale-token", upstream.requests[0].authorization)
	assert.Equal(t, "Bearer rotated-token", upstream.requests[1].authorization, "retry must carry the rotated token")
	assert.Equal(t, `{"model":"claude-fable-5"}`, upstream.requests[1].body, "retry must replay the body")

	assert.Equal(t, "rotated-token", opts.ApiKey, "adopted token must stick for future requests")
	assert.Equal(t, "rt-2", opts.RefreshToken)
}

// TestTokenRefreshTransport_401WithoutNewerTokensPassesThrough: the store
// agrees with the token we just used, so the 401 is a genuine auth failure and
// must surface unchanged (this is the "please reconnect" path).
func TestTokenRefreshTransport_401WithoutNewerTokensPassesThrough(t *testing.T) {
	upstream := &scriptedRoundTripper{statuses: []int{http.StatusUnauthorized}}

	opts := &llm.DriverOptions{
		ApiKey:         "current-token",
		UserID:         "user-1",
		RefreshToken:   "rt-1",
		TokenExpiresAt: time.Now().Add(time.Hour),
		TokenReloader: func() (*llm.OAuthTokens, error) {
			return &llm.OAuthTokens{
				AccessToken:  "current-token", // unchanged
				RefreshToken: "rt-1",
				ExpiresAt:    time.Now().Add(time.Hour),
			}, nil
		},
	}
	transport := newTestTokenRefreshTransport(opts, upstream)

	resp, err := transport.RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Len(t, upstream.requests, 1, "no retry when the store has nothing newer")
}

// TestTokenRefreshTransport_401WithEmptyStorePassesThrough: no persisted
// tokens at all (user disconnected Claude) — surface the 401.
func TestTokenRefreshTransport_401WithEmptyStorePassesThrough(t *testing.T) {
	upstream := &scriptedRoundTripper{statuses: []int{http.StatusUnauthorized}}

	opts := &llm.DriverOptions{
		ApiKey:         "current-token",
		UserID:         "user-1",
		RefreshToken:   "rt-1",
		TokenExpiresAt: time.Now().Add(time.Hour),
		TokenReloader: func() (*llm.OAuthTokens, error) {
			return nil, nil
		},
	}
	transport := newTestTokenRefreshTransport(opts, upstream)

	resp, err := transport.RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Len(t, upstream.requests, 1)
}

// TestTokenRefreshTransport_PreflightRefreshUpdatesAuthorization: an expired
// access token triggers the coordinated refresher before the request goes out,
// and the request carries the refreshed token.
func TestTokenRefreshTransport_PreflightRefreshUpdatesAuthorization(t *testing.T) {
	upstream := &scriptedRoundTripper{statuses: []int{http.StatusOK}}

	var heldSeen llm.OAuthTokens
	refreshCalls := 0
	opts := &llm.DriverOptions{
		ApiKey:         "expired-token",
		UserID:         "user-1",
		RefreshToken:   "rt-1",
		TokenExpiresAt: time.Now().Add(-time.Minute),
		TokenRefresher: func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
			refreshCalls++
			heldSeen = held
			return llm.OAuthTokens{
				AccessToken:  "fresh-token",
				RefreshToken: "rt-2",
				ExpiresAt:    time.Now().Add(8 * time.Hour),
			}, nil
		},
	}
	transport := newTestTokenRefreshTransport(opts, upstream)

	resp, err := transport.RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, 1, refreshCalls)
	assert.Equal(t, "expired-token", heldSeen.AccessToken, "refresher must receive the held state")
	assert.Equal(t, "rt-1", heldSeen.RefreshToken)

	require.Len(t, upstream.requests, 1)
	assert.Equal(t, "Bearer fresh-token", upstream.requests[0].authorization)
	assert.Equal(t, "fresh-token", opts.ApiKey)
	assert.Equal(t, "rt-2", opts.RefreshToken)
}

// TestTokenRefreshTransport_PreflightRefreshFailureContinuesWithHeldToken: a
// failed refresh must not fail the request — the held token is sent and the
// API decides (matching pre-existing behavior).
func TestTokenRefreshTransport_PreflightRefreshFailureContinuesWithHeldToken(t *testing.T) {
	upstream := &scriptedRoundTripper{statuses: []int{http.StatusOK}}

	opts := &llm.DriverOptions{
		ApiKey:         "held-token",
		UserID:         "user-1",
		RefreshToken:   "rt-1",
		TokenExpiresAt: time.Now().Add(-time.Minute),
		TokenRefresher: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
			return llm.OAuthTokens{}, fmt.Errorf("claude token refresh failed: boom")
		},
	}
	transport := newTestTokenRefreshTransport(opts, upstream)

	resp, err := transport.RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, upstream.requests, 1)
	assert.Equal(t, "Bearer held-token", upstream.requests[0].authorization)
	assert.Equal(t, "held-token", opts.ApiKey, "held token must remain in place after a failed refresh")
}
