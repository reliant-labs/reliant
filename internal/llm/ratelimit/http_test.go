// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
)

func TestRetryer_BodyRetryAfterIsHonoredAndCappedLikeTheHeader(t *testing.T) {
	r := Retryer{Provider: "T", Now: func() time.Time { return fixedNow }}

	wait, retry, err := r.Decide(1, Failure{Err: errors.New("429"), Status: 429, RetryAfter: 7 * time.Second})
	require.NoError(t, err)
	assert.True(t, retry)
	assert.Equal(t, 7*time.Second, wait.Delay)

	// The header wins when both are present.
	wait, _, _ = r.Decide(1, Failure{Err: errors.New("429"), Status: 429, Header: hdrs("Retry-After", "2"), RetryAfter: 7 * time.Second})
	assert.Equal(t, 2*time.Second, wait.Delay)

	_, retry, err = r.Decide(1, Failure{Err: errors.New("429"), Status: 429, RetryAfter: time.Hour})
	assert.False(t, retry)
	var ule *UsageLimitError
	require.ErrorAs(t, err, &ule, "a body wait past the cap is a usage limit, as a header one is")
	assert.Equal(t, fixedNow.Add(time.Hour), ule.ResetAt)
}

func TestRetryer_ResetHintLengthensBackoffButNeverEndsTheLadder(t *testing.T) {
	r := Retryer{Provider: "T", Jitter: func(time.Duration) time.Duration { return 0 }}

	wait, retry, err := r.Decide(1, Failure{Err: errors.New("429"), Status: 429, ResetHint: 9 * time.Second})
	require.NoError(t, err)
	assert.True(t, retry)
	assert.Equal(t, 9*time.Second, wait.Delay)

	wait, retry, err = r.Decide(1, Failure{Err: errors.New("429"), Status: 429, ResetHint: 6 * time.Hour})
	require.NoError(t, err)
	assert.True(t, retry, "a token bucket's refill time is a hint, not a usage window")
	assert.Equal(t, DefaultMaxBackoff, wait.Delay)

	wait, _, _ = r.Decide(3, Failure{Err: errors.New("429"), Status: 429, ResetHint: time.Second})
	assert.Equal(t, 8*time.Second, wait.Delay, "a hint shorter than the backoff changes nothing")
}

func TestRetryer_FailureReasonNamesTheWait(t *testing.T) {
	r := Retryer{Provider: "T"}
	wait, _, _ := r.Decide(1, Failure{Err: errors.New("500"), Status: 500, Reason: "transient_upstream_500"})
	assert.Equal(t, "transient_upstream_500", wait.Reason)
	wait, _, _ = r.Decide(1, Failure{Err: errors.New("500"), Status: 500})
	assert.Equal(t, "http_500", wait.Reason)
}

func TestResetHint(t *testing.T) {
	for name, tc := range map[string]struct {
		h    http.Header
		want time.Duration
	}{
		"none":                      {http.Header{}, 0},
		"openai exhausted tokens":   {hdrs("x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s", "x-ratelimit-remaining-requests", "10", "x-ratelimit-reset-requests", "1s"), 6 * time.Minute},
		"openai bucket not empty":   {hdrs("x-ratelimit-remaining-requests", "3", "x-ratelimit-reset-requests", "20s"), 0},
		"openrouter epoch ms":       {hdrs("X-RateLimit-Reset", fmt.Sprint(fixedNow.Add(30*time.Second).UnixMilli()), "X-RateLimit-Remaining", "0"), 30 * time.Second},
		"bare seconds":              {hdrs("x-ratelimit-reset", "12"), 12 * time.Second},
		"bare reset, requests left": {hdrs("x-ratelimit-reset", "12", "x-ratelimit-remaining", "5"), 0},
		"reset already in the past": {hdrs("X-RateLimit-Reset", fmt.Sprint(fixedNow.Add(-time.Minute).UnixMilli())), 0},
	} {
		assert.Equal(t, tc.want, ResetHint(tc.h, fixedNow), name)
	}
}

func TestIsTransportError(t *testing.T) {
	assert.True(t, IsTransportError(&url.Error{Op: "Post", URL: "http://x", Err: errors.New("connection refused")}))
	assert.True(t, IsTransportError(fmt.Errorf("read: %w", llm.ErrStreamIdleTimeout)))
	assert.False(t, IsTransportError(errors.New("invalid character '<' looking for beginning of value")))
	assert.False(t, IsTransportError(nil))
}

// OpenHTTP is the raw-HTTP drivers' request-open: a 429 with Retry-After is
// retried once after exactly that wait, published before it is slept; a 400
// is returned at once; a request that cannot be built is not retried.
func TestOpenHTTP(t *testing.T) {
	newLadder := func(sleeps *[]time.Duration) Retryer {
		return Retryer{
			Provider: "T", Jitter: func(time.Duration) time.Duration { return 0 },
			Sleep: func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
		}
	}
	describe := func(resp *http.Response, body []byte, err error) Failure {
		if resp == nil {
			return HTTPFailure(nil, err)
		}
		return HTTPFailure(resp, fmt.Errorf("status %d: %s", resp.StatusCode, body))
	}

	t.Run("429 then 200", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				w.Header().Set("Retry-After", "3")
				http.Error(w, "slow down", http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()
		var sleeps []time.Duration
		var waits []llm.RetryWait
		resp, err := newLadder(&sleeps).OpenHTTP(context.Background(), srv.Client().Do, func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, srv.URL, nil)
		}, describe, func(w llm.RetryWait) error { waits = append(waits, w); return nil })
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.EqualValues(t, 2, hits.Load())
		assert.Equal(t, []time.Duration{3 * time.Second}, sleeps)
		require.Len(t, waits, 1)
		assert.Equal(t, http.StatusTooManyRequests, waits[0].StatusCode)
	})

	t.Run("400 is not retried", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			http.Error(w, "prompt is too long", http.StatusBadRequest)
		}))
		defer srv.Close()
		var sleeps []time.Duration
		_, err := newLadder(&sleeps).OpenHTTP(context.Background(), srv.Client().Do, func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, srv.URL, nil)
		}, describe, nil)
		require.ErrorContains(t, err, "status 400: prompt is too long")
		assert.EqualValues(t, 1, hits.Load())
		assert.Empty(t, sleeps)
	})

	t.Run("a request that cannot be built is final", func(t *testing.T) {
		var sleeps []time.Duration
		builds := 0
		_, err := newLadder(&sleeps).OpenHTTP(context.Background(), http.DefaultClient.Do, func() (*http.Request, error) {
			builds++
			return nil, errors.New("bad url")
		}, describe, nil)
		require.ErrorContains(t, err, "bad url")
		assert.Equal(t, 1, builds)
	})
}
