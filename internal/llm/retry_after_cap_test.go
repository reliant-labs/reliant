// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usageLimitServer answers every request the way a Claude subscription does
// once its usage window is spent: 429, a rate_limit_error body, and a
// Retry-After pointing at the window reset hours away.
func usageLimitServer(t *testing.T, retryAfter string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your usage limit."}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestAnthropicSDKClient_UsageLimitSurfacesInsteadOfSleeping is the regression
// for chats that stalled for exactly the 10-minute progress timeout, over and
// over, whenever the Claude subscription ran out of credit.
//
// The SDK treats 429 as retryable and sleeps for Retry-After VERBATIM before
// its next attempt. A usage-limit 429 carries a Retry-After of hours, so the
// request parked inside the SDK with no response body in existence — invisible
// to the byte-idle and content-stall guards, which only watch bodies. The
// progress backstop then cancelled the context, the SDK returned ctx.Err()
// instead of the 429, and the provider's own explanation was thrown away.
//
// The guarantee under test: the caller gets the provider's error, promptly.
func TestAnthropicSDKClient_UsageLimitSurfacesInsteadOfSleeping(t *testing.T) {
	srv, hits := usageLimitServer(t, "18000") // five hours, in seconds

	client := NewAnthropicSDKClient(
		anthropicopt.WithBaseURL(srv.URL),
		anthropicopt.WithAPIKey("test-key"),
	)

	// Generous against a correct implementation, far short of the Retry-After.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	stream := client.Messages.NewStreaming(ctx, anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.Model("claude-test"),
		MaxTokens: 16,
		Messages:  []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("hi"))},
	})
	for stream.Next() {
	}
	err := stream.Err()
	elapsed := time.Since(start)

	require.Error(t, err, "a usage-limited request must fail, not succeed empty")
	require.False(t, errors.Is(err, context.DeadlineExceeded),
		"the request slept on Retry-After until the caller gave up (%s); the provider's 429 was discarded", elapsed)
	assert.Less(t, elapsed, 10*time.Second, "a usage-limit 429 must surface promptly")

	var apiErr *anthropicsdk.Error
	require.ErrorAs(t, err, &apiErr, "the caller must receive the provider's own error")
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.Contains(t, err.Error(), "usage limit", "the provider's explanation must reach the caller")
	assert.GreaterOrEqual(t, hits.Load(), int32(1))
}

// The claude-code driver builds its own transport chain and reaches the guard
// through WrapWithIdleTimeout, not StreamingHTTPClient. That is the driver the
// incident actually ran on, so the cap must hold on this path too.
func TestWrapWithIdleTimeout_UsageLimitSurfacesInsteadOfSleeping(t *testing.T) {
	srv, _ := usageLimitServer(t, "18000")

	client := anthropicsdk.NewClient(
		anthropicopt.WithHTTPClient(&http.Client{Transport: WrapWithIdleTimeout(http.DefaultTransport)}),
		anthropicopt.WithBaseURL(srv.URL),
		anthropicopt.WithAPIKey("test-key"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	stream := client.Messages.NewStreaming(ctx, anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.Model("claude-test"),
		MaxTokens: 16,
		Messages:  []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("hi"))},
	})
	for stream.Next() {
	}
	err := stream.Err()

	require.Error(t, err)
	require.False(t, errors.Is(err, context.DeadlineExceeded),
		"slept on Retry-After for %s through WrapWithIdleTimeout", time.Since(start))
	var apiErr *anthropicsdk.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    time.Duration
		ok      bool
	}{
		{"absent", nil, 0, false},
		{"seconds", map[string]string{"Retry-After": "120"}, 120 * time.Second, true},
		{"fractional seconds", map[string]string{"Retry-After": "1.5"}, 1500 * time.Millisecond, true},
		{"milliseconds preferred", map[string]string{"Retry-After-Ms": "250", "Retry-After": "9999"}, 250 * time.Millisecond, true},
		{"garbage", map[string]string{"Retry-After": "soon"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			got, ok := parseRetryAfter(h)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("http-date", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", time.Now().Add(3*time.Hour).UTC().Format(http.TimeFormat))
		got, ok := parseRetryAfter(h)
		require.True(t, ok)
		assert.InDelta(t, (3 * time.Hour).Seconds(), got.Seconds(), 5)
	})
}

func TestCapRetryAfter(t *testing.T) {
	respWith := func(retryAfter string) *http.Response {
		h := http.Header{}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}
	}

	long := respWith("3600")
	capRetryAfter(long)
	assert.Equal(t, "false", long.Header.Get("x-should-retry"), "a long Retry-After must decline the in-place retry")
	assert.Equal(t, "3600", long.Header.Get("Retry-After"), "the original delay must stay readable by the caller")

	short := respWith("5")
	capRetryAfter(short)
	assert.Empty(t, short.Header.Get("x-should-retry"), "a short Retry-After must be left for the SDK to honour")

	none := respWith("")
	capRetryAfter(none)
	assert.Empty(t, none.Header.Get("x-should-retry"), "no Retry-After means no opinion")

	capRetryAfter(nil) // must not panic
}

// TestAnthropicSDKClient_ShortRetryAfterStillRetries is the false-positive
// guard. An ordinary 429 with a short Retry-After is a real transient, and the
// SDK retrying it in place is correct behaviour that must survive the fix.
func TestAnthropicSDKClient_ShortRetryAfterStillRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	t.Cleanup(srv.Close)

	client := NewAnthropicSDKClient(
		anthropicopt.WithBaseURL(srv.URL),
		anthropicopt.WithAPIKey("test-key"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream := client.Messages.NewStreaming(ctx, anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.Model("claude-test"),
		MaxTokens: 16,
		Messages:  []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("hi"))},
	})
	for stream.Next() {
	}
	require.NoError(t, stream.Err(), "a short Retry-After 429 must still be retried through to success")
	assert.Equal(t, int32(2), hits.Load(), "exactly one retry")
}
