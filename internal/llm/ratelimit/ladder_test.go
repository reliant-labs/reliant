// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRetryer(sleeps *[]time.Duration) Retryer {
	return Retryer{
		Provider: "Test",
		Now:      func() time.Time { return fixedNow },
		Jitter:   func(time.Duration) time.Duration { return 0 },
		Sleep: func(_ context.Context, d time.Duration) error {
			*sleeps = append(*sleeps, d)
			return nil
		},
	}
}

func TestRetryer_BackoffLadderWithoutRetryAfter(t *testing.T) {
	var sleeps []time.Duration
	r := testRetryer(&sleeps)
	var waits []llm.RetryWait
	attempts := 0
	err := r.Run(context.Background(), func(int) (Failure, bool) {
		attempts++
		return Failure{Err: errors.New("503"), Status: 503}, false
	}, func(w llm.RetryWait) error { waits = append(waits, w); return nil })

	require.Error(t, err)
	assert.Equal(t, models.MaxRetries+1, attempts)
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute, time.Minute}, sleeps)
	require.Len(t, waits, models.MaxRetries)
	assert.Equal(t, 1, waits[0].Attempt)
	assert.Equal(t, "http_503", waits[0].Reason)
	assert.Equal(t, models.MaxRetries, waits[0].MaxAttempts)
}

func TestRetryer_WaitIsPublishedBeforeItIsSlept(t *testing.T) {
	var order []string
	r := Retryer{
		Provider: "Test", Jitter: func(time.Duration) time.Duration { return 0 },
		Sleep: func(context.Context, time.Duration) error { order = append(order, "sleep"); return nil },
	}
	n := 0
	require.NoError(t, r.Run(context.Background(), func(int) (Failure, bool) {
		n++
		if n == 1 {
			return Failure{Err: errors.New("429"), Status: 429, Header: hdrs("Retry-After", "3")}, false
		}
		return Failure{}, true
	}, func(llm.RetryWait) error { order = append(order, "emit"); return nil }))
	assert.Equal(t, []string{"emit", "sleep"}, order)
}

func TestRetryer_HonorsRetryAfterForms(t *testing.T) {
	r := Retryer{Provider: "T", Now: func() time.Time { return fixedNow }}
	for name, tc := range map[string]struct {
		h    http.Header
		want time.Duration
	}{
		"seconds":      {hdrs("Retry-After", "7"), 7 * time.Second},
		"millis wins":  {hdrs("Retry-After", "30", "Retry-After-Ms", "250"), 250 * time.Millisecond},
		"http date":    {hdrs("Retry-After", fixedNow.Add(90*time.Second).Format(http.TimeFormat)), 90 * time.Second},
		"fractional s": {hdrs("Retry-After", "1.5"), 1500 * time.Millisecond},
	} {
		wait, retry, err := r.Decide(1, Failure{Err: errors.New("429"), Status: 429, Header: tc.h})
		require.NoError(t, err, name)
		assert.True(t, retry, name)
		assert.Equal(t, tc.want, wait.Delay, name)
	}
}

func TestRetryer_ExplicitUsageLimitEndsTheLadderImmediately(t *testing.T) {
	var sleeps []time.Duration
	r := testRetryer(&sleeps)
	reset := time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC)
	calls := 0
	err := r.Run(context.Background(), func(int) (Failure, bool) {
		calls++
		return Failure{
			Err: errors.New("429"), Status: 429, Header: hdrs("Retry-After", "2"),
			UsageLimit: true, Limit: LimitInfo{ResetAt: reset, Window: "five_hour"},
		}, false
	}, nil)

	assert.Equal(t, 1, calls)
	assert.Empty(t, sleeps)
	var limit *UsageLimitError
	require.ErrorAs(t, err, &limit)
	assert.Equal(t, reset, limit.ResetAt)
	kind, payload, found := chatmarkers.Extract(err.Error())
	require.True(t, found)
	assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
	assert.Equal(t, "2026-10-18T17:00:00Z", payload)
	assert.Contains(t, chatmarkers.ProviderUsageLimitSummary(err.Error()), "5-hour window")
}

func TestRetryer_RetryAfterPastTheCapIsAUsageLimitWithResetFromRetryAfter(t *testing.T) {
	var sleeps []time.Duration
	r := testRetryer(&sleeps)
	_, retry, err := r.Decide(1, Failure{Err: errors.New("429"), Status: 429, Header: hdrs("Retry-After", "7200")})
	assert.False(t, retry)
	var limit *UsageLimitError
	require.ErrorAs(t, err, &limit)
	assert.Equal(t, fixedNow.Add(2*time.Hour), limit.ResetAt)
	assert.Equal(t, 2*time.Hour, limit.RetryAfter)
	_, payload, _ := chatmarkers.Extract(err.Error())
	assert.Equal(t, "2026-10-18T14:00:00Z", payload)

	// exactly at the cap is still slept out
	_, retry, err = r.Decide(1, Failure{Err: errors.New("429"), Status: 429, Header: hdrs("Retry-After", "300")})
	require.NoError(t, err)
	assert.True(t, retry)
}

func TestRetryer_UsageLimitWithUnknownResetHasEmptyPayload(t *testing.T) {
	r := testRetryer(new([]time.Duration))
	_, _, err := r.Decide(1, Failure{Err: errors.New("429"), Status: 429, UsageLimit: true})
	kind, payload, found := chatmarkers.Extract(err.Error())
	require.True(t, found)
	assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
	assert.Empty(t, payload, "the contract: reset time RFC 3339 UTC, or empty when unknown")
	assert.Contains(t, err.Error(), chatmarkers.ProviderUsageLimitLead)
}

func TestRetryer_NonRetryableAndFatal(t *testing.T) {
	r := testRetryer(new([]time.Duration))
	orig := errors.New("400")
	_, retry, err := r.Decide(1, Failure{Err: orig, Status: 400})
	assert.False(t, retry)
	assert.Same(t, orig, err)

	quota := errors.New("insufficient_quota")
	_, retry, err = r.Decide(1, Failure{Err: quota, Status: 429, Fatal: true})
	assert.False(t, retry)
	assert.Same(t, quota, err, "a fatal 429 is returned unchanged, with no retries and no usage-limit marker")

	_, retry, _ = r.Decide(1, Failure{Err: context.Canceled})
	assert.False(t, retry)
	wait, retry, err := r.Decide(2, Failure{Err: errors.New("tls: bad record MAC")})
	require.NoError(t, err)
	assert.True(t, retry)
	assert.Equal(t, "connection_error", wait.Reason)
	assert.Equal(t, 4*time.Second, wait.Delay)
}

func TestRetryer_ContextCancelDuringSleepStopsTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := Retryer{Provider: "T", Jitter: func(time.Duration) time.Duration { return 0 }}
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx, func(int) (Failure, bool) {
			calls++
			return Failure{Err: errors.New("503"), Status: 503}, false
		}, func(llm.RetryWait) error { cancel(); return nil })
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

type recordingObserver struct {
	mu    sync.Mutex
	snaps []Snapshot
}

func (r *recordingObserver) ObserveRateLimits(_ context.Context, s Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, s)
}

func TestMiddleware_ObservesSuccessAndErrorResponses(t *testing.T) {
	obs := &recordingObserver{}
	ctx := WithObserver(context.Background(), obs)
	mw := Middleware("codex", ParseCodex)

	for _, status := range []int{200, 429} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x", nil)
		res, err := mw(req, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: hdrs("x-codex-primary-used-percent", "55", "x-codex-primary-reset-at", "1704069000")}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, status, res.StatusCode)
	}
	// a response without rate-limit data and a transport error report nothing
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x", nil)
	_, _ = mw(req, func(*http.Request) (*http.Response, error) { return &http.Response{Header: hdrs("a", "b")}, nil })
	_, err := mw(req, func(*http.Request) (*http.Response, error) { return nil, errors.New("dial") })
	require.Error(t, err)

	require.Len(t, obs.snaps, 2)
	assert.Equal(t, "codex", obs.snaps[0].Provider)
	assert.Equal(t, StatusAllowed, obs.snaps[0].Status)
}

func TestObserve_DefaultIsANoOp(t *testing.T) {
	Observe(context.Background(), "openai", Snapshot{Status: StatusAllowed}) // must not panic
}
