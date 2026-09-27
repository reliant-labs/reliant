// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamRetryDecision(t *testing.T) {
	apiErr := func(status int, header http.Header) error {
		return &anthropic.Error{StatusCode: status, Response: &http.Response{StatusCode: status, Header: header}}
	}
	hdr := func(k, v string) http.Header { h := http.Header{}; h.Set(k, v); return h }

	t.Run("429 honors Retry-After seconds", func(t *testing.T) {
		wait, retry, err := streamRetryDecision(1, apiErr(429, hdr("Retry-After", "3")))
		require.NoError(t, err)
		assert.True(t, retry)
		assert.Equal(t, 3*time.Second, wait.Delay)
		assert.Equal(t, 429, wait.StatusCode)
		assert.Equal(t, "http_429", wait.Reason)
	})
	t.Run("retry-after-ms wins over Retry-After", func(t *testing.T) {
		h := hdr("Retry-After", "30")
		h.Set("Retry-After-Ms", "250")
		wait, retry, _ := streamRetryDecision(1, apiErr(529, h))
		assert.True(t, retry)
		assert.Equal(t, 250*time.Millisecond, wait.Delay)
	})
	t.Run("a Retry-After past the cap is surfaced, not slept", func(t *testing.T) {
		_, retry, err := streamRetryDecision(1, apiErr(429, hdr("Retry-After", "3600")))
		assert.False(t, retry)
		var tooLong *RetryAfterTooLongError
		require.ErrorAs(t, err, &tooLong)
		assert.Equal(t, time.Hour, tooLong.RetryAfter)
	})
	t.Run("a client error is not retried", func(t *testing.T) {
		orig := apiErr(400, nil)
		_, retry, err := streamRetryDecision(1, orig)
		assert.False(t, retry)
		assert.Same(t, orig, err)
	})
	t.Run("a connection error is retried with backoff", func(t *testing.T) {
		wait, retry, err := streamRetryDecision(2, errors.New("remote error: tls: bad record MAC"))
		require.NoError(t, err)
		assert.True(t, retry)
		assert.Equal(t, "connection_error", wait.Reason)
		assert.GreaterOrEqual(t, wait.Delay, 4*time.Second)
	})
	t.Run("cancellation is never retried", func(t *testing.T) {
		_, retry, _ := streamRetryDecision(1, fmt.Errorf("stream: %w", context.Canceled))
		assert.False(t, retry)
	})
	t.Run("the ladder ends at MaxRetries", func(t *testing.T) {
		_, retry, _ := streamRetryDecision(models.MaxRetries+1, apiErr(503, nil))
		assert.False(t, retry)
	})
}

const okSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func testBaseClient(url string) *baseClient {
	return &baseClient{client: llm.NewAnthropicSDKClient(option.WithBaseURL(url), option.WithAPIKey("k"))}
}

func collect(t *testing.T, ch <-chan llm.DriverEvent) []llm.DriverEvent {
	t.Helper()
	var out []llm.DriverEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

func params() anthropic.MessageNewParams {
	return anthropic.MessageNewParams{Model: "m", MaxTokens: 10,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))}}
}

// TestStreamPublishesRetryWaitInsteadOfSleepingInTheSDK is the 2026-09-27
// stall: Anthropic answers 429 with a Retry-After. Before, the SDK slept it
// out inside NewStreaming and nothing upstream could see the wait. Now the
// driver emits EventRetryWait (which CallLLM records as provider_backoff),
// retries itself, and the SDK makes exactly one request per attempt.
func TestStreamPublishesRetryWaitInsteadOfSleepingInTheSDK(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After-Ms", "20")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(okSSE))
	}))
	defer srv.Close()

	events := collect(t, testBaseClient(srv.URL).streamResponseInternal(context.Background(), params()))

	require.NotEmpty(t, events)
	require.Equal(t, llm.EventRetryWait, events[0].Type, "the wait must be published before it is taken")
	assert.Equal(t, 429, events[0].Retry.StatusCode)
	assert.Equal(t, 20*time.Millisecond, events[0].Retry.Delay)
	assert.Equal(t, llm.EventComplete, events[len(events)-1].Type)
	for _, ev := range events {
		assert.NotEqual(t, llm.EventError, ev.Type, "a retried 429 must not surface as an error: %v", ev.Error)
	}
	assert.Equal(t, int32(2), hits.Load(), "one 429 then one success — any more means the SDK is retrying underneath")
}

// TestStreamSurfacesARetryAfterPastTheCap: a subscription limit can ask for
// hours. Sleeping that out inside one activity is indistinguishable from a
// hang, so the driver fails the request at once with the requested wait.
func TestStreamSurfacesARetryAfterPastTheCap(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "7200")
		w.Header().Set("anthropic-ratelimit-unified-representative-claim", "seven_day")
		w.Header().Set("anthropic-ratelimit-unified-overage-disabled-reason", "out_of_credits")
		w.Header().Set("anthropic-ratelimit-unified-reset", "1790712000")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"limit"}}`))
	}))
	defer srv.Close()

	start := time.Now()
	events := collect(t, testBaseClient(srv.URL).streamResponseInternal(context.Background(), params()))

	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, int32(1), hits.Load())
	var sawErr bool
	for _, ev := range events {
		if ev.Type == llm.EventError {
			var tooLong *RetryAfterTooLongError
			require.ErrorAs(t, ev.Error, &tooLong)
			assert.Equal(t, 2*time.Hour, tooLong.RetryAfter)
			assert.Equal(t, time.Unix(1790712000, 0).UTC(), tooLong.ResetAt, "the provider's own reset time wins over now+Retry-After")
			// What the chat shows: the marker the workflow routes on, and a
			// sentence that names the window and when it lifts.
			kind, payload, found := chatmarkers.Extract(ev.Error.Error())
			require.True(t, found)
			assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
			assert.Equal(t, "2026-09-29T20:00:00Z", payload)
			summary := chatmarkers.ProviderUsageLimitSummary(ev.Error.Error())
			assert.Contains(t, summary, "7-day window")
			assert.Contains(t, summary, "overage unavailable: out of credits")
			assert.Contains(t, summary, "resets Tue Sep 29 20:00 UTC")
			sawErr = true
		}
	}
	assert.True(t, sawErr, "the capped wait must surface as an error event")
}
