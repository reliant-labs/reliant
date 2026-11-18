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

	// Deliberately a non-canonical key. lowercaseHeaderTransport writes
	// req.Header["authorization"] by direct map access to reproduce Claude
	// Code's exact wire casing, so looking it up canonically would miss the
	// header these tests exist to assert on.
	auth := ""
	//nolint:staticcheck // SA1008: lowercase casing is the behavior under test
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

// fixedNow is the clock the reliant#2833 tests run on: every expiry is set
// relative to it and the transport's pre-flight check reads it, so no
// assertion depends on the wall clock.
var fixedNow = time.Date(2026, 11, 17, 19, 2, 41, 0, time.UTC)

func newFixedClockTransport(opts *llm.DriverOptions, upstream http.RoundTripper) *tokenRefreshTransport {
	tr := newTestTokenRefreshTransport(opts, upstream)
	tr.now = func() time.Time { return fixedNow }
	return tr
}

// reliant#2833: the provider answered 401 "OAuth access token has expired"
// while the stored expiry still called the token valid, and the store held the
// same token. The transport refreshes through the coordinator once and retries
// once with the new token, instead of failing the run terminally.
func TestTokenRefreshTransport_401TheStoreCannotExplainRefreshesAndRetriesOnce(t *testing.T) {
	upstream := &scriptedRoundTripper{statuses: []int{http.StatusUnauthorized, http.StatusOK}}

	var refreshedWith []llm.OAuthTokens
	opts := &llm.DriverOptions{
		ApiKey:         "current-token",
		UserID:         "user-1",
		RefreshToken:   "rt-1",
		TokenExpiresAt: fixedNow.Add(time.Hour), // looks valid: no pre-flight refresh
		TokenReloader: func() (*llm.OAuthTokens, error) {
			return &llm.OAuthTokens{AccessToken: "current-token", RefreshToken: "rt-1", ExpiresAt: fixedNow.Add(time.Hour)}, nil
		},
		TokenRefresher: func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
			refreshedWith = append(refreshedWith, held)
			return llm.OAuthTokens{AccessToken: "refreshed-token", RefreshToken: "rt-2", ExpiresAt: fixedNow.Add(8 * time.Hour)}, nil
		},
	}
	transport := newFixedClockTransport(opts, upstream)

	resp, err := transport.RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the retry with the refreshed token succeeds")
	assert.False(t, llm.OAuthRefreshedStillRejectedResponse(resp), "a recovered request is not marked")
	require.Len(t, refreshedWith, 1, "refreshed exactly once")
	assert.Equal(t, "current-token", refreshedWith[0].AccessToken, "the coordinator is told which token was refused")
	require.Len(t, upstream.requests, 2)
	assert.Equal(t, "Bearer refreshed-token", upstream.requests[1].authorization)
	assert.Equal(t, `{"model":"claude-fable-5"}`, upstream.requests[1].body, "retry must replay the body")
	assert.Equal(t, "refreshed-token", opts.ApiKey, "the refreshed token sticks for future requests")
	assert.Equal(t, "rt-2", opts.RefreshToken)
	assert.Equal(t, fixedNow.Add(8*time.Hour), opts.TokenExpiresAt)
}

// A refresh that fails (the provider refused it: the coordinator then marks
// the account needs-sign-in), or that returns the same token, leaves the 401 to
// surface, with no retry. A retry that is refused again surfaces too: one
// refresh and one retry per request, never a loop. Each surfaced 401 is marked
// as having survived the refresh, which is what lets the activity tell the
// user to reconnect.
func TestTokenRefreshTransport_401RefreshThatCannotHelpPassesThrough(t *testing.T) {
	sameStore := func() (*llm.OAuthTokens, error) {
		return &llm.OAuthTokens{AccessToken: "current-token", RefreshToken: "rt-1", ExpiresAt: fixedNow.Add(time.Hour)}, nil
	}
	cases := []struct {
		name     string
		statuses []int
		refresh  func(llm.OAuthTokens) (llm.OAuthTokens, error)
		requests int
	}{
		{
			name:     "refresh refused",
			statuses: []int{http.StatusUnauthorized},
			refresh: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
				return llm.OAuthTokens{}, fmt.Errorf("claude token refresh failed: %w", llm.ErrRefreshRejected)
			},
			requests: 1,
		},
		{
			name:     "same token back",
			statuses: []int{http.StatusUnauthorized},
			refresh: func(h llm.OAuthTokens) (llm.OAuthTokens, error) {
				return h, nil
			},
			requests: 1,
		},
		{
			name:     "refused again after the refresh",
			statuses: []int{http.StatusUnauthorized, http.StatusUnauthorized},
			refresh: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
				return llm.OAuthTokens{AccessToken: "refreshed-token", RefreshToken: "rt-2", ExpiresAt: fixedNow.Add(time.Hour)}, nil
			},
			requests: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &scriptedRoundTripper{statuses: tc.statuses}
			refreshes := 0
			opts := &llm.DriverOptions{
				ApiKey: "current-token", UserID: "user-1", RefreshToken: "rt-1",
				TokenExpiresAt: fixedNow.Add(time.Hour),
				TokenReloader:  sameStore,
				TokenRefresher: func(h llm.OAuthTokens) (llm.OAuthTokens, error) { refreshes++; return tc.refresh(h) },
			}
			resp, err := newFixedClockTransport(opts, upstream).RoundTrip(newMessagesRequest(t))
			require.NoError(t, err)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			assert.True(t, llm.OAuthRefreshedStillRejectedResponse(resp), "a 401 that survived the refresh is marked")
			assert.Equal(t, 1, refreshes, "one refresh per refused request")
			assert.Len(t, upstream.requests, tc.requests)
		})
	}
}

// Without a refresh, no mark: a 401 the store explained (a rotation elsewhere)
// and retried, or one with no refresher at all, keeps its old handling.
func TestTokenRefreshTransport_401WithoutARefreshIsNotMarked(t *testing.T) {
	t.Run("no refresher", func(t *testing.T) {
		upstream := &scriptedRoundTripper{statuses: []int{http.StatusUnauthorized}}
		opts := &llm.DriverOptions{
			ApiKey: "current-token", UserID: "user-1", RefreshToken: "rt-1",
			TokenExpiresAt: fixedNow.Add(time.Hour),
			TokenReloader: func() (*llm.OAuthTokens, error) {
				return &llm.OAuthTokens{AccessToken: "current-token", RefreshToken: "rt-1", ExpiresAt: fixedNow.Add(time.Hour)}, nil
			},
		}
		resp, err := newFixedClockTransport(opts, upstream).RoundTrip(newMessagesRequest(t))
		require.NoError(t, err)
		assert.False(t, llm.OAuthRefreshedStillRejectedResponse(resp))
	})
	t.Run("rotated elsewhere, retried, refused", func(t *testing.T) {
		upstream := &scriptedRoundTripper{statuses: []int{http.StatusUnauthorized, http.StatusUnauthorized}}
		opts := &llm.DriverOptions{
			ApiKey: "stale-token", UserID: "user-1", RefreshToken: "rt-1",
			TokenExpiresAt: fixedNow.Add(time.Hour),
			TokenReloader: func() (*llm.OAuthTokens, error) {
				return &llm.OAuthTokens{AccessToken: "rotated-token", RefreshToken: "rt-2", ExpiresAt: fixedNow.Add(time.Hour)}, nil
			},
			TokenRefresher: func(llm.OAuthTokens) (llm.OAuthTokens, error) {
				t.Error("a 401 the store explained must not refresh")
				return llm.OAuthTokens{}, nil
			},
		}
		resp, err := newFixedClockTransport(opts, upstream).RoundTrip(newMessagesRequest(t))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.False(t, llm.OAuthRefreshedStillRejectedResponse(resp))
	})
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
