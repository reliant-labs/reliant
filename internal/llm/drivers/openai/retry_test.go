// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func responsesTestClient(url string) *OpenaiClient {
	return &OpenaiClient{
		Options: llm.DriverOptions{Model: models.Model{ID: "gpt-5", APIModel: "gpt-5", PreferredEndpoint: "responses"}, MaxTokens: 100},
		Client: llm.NewOpenAISDKClient(
			option.WithBaseURL(url), option.WithAPIKey("k"),
			option.WithMiddleware(ratelimit.Middleware("openai", ratelimit.ParseOpenAI)),
		),
		ladder: ratelimit.Retryer{
			Jitter: func(time.Duration) time.Duration { return 0 },
			Sleep:  func(context.Context, time.Duration) error { return nil },
		},
	}
}

func drainOpenAI(t *testing.T, c *OpenaiClient) []llm.DriverEvent {
	t.Helper()
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	var out []llm.DriverEvent
	timeout := time.After(10 * time.Second)
	ch := c.StreamResponse(context.Background(), []string{"p"}, msgs, nil)
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

const openaiOKStream = "event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"

func TestResponsesStream_PublishesRetryWaitAndHonorsRetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "4")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"requests","code":"rate_limit_exceeded","message":"slow"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(openaiOKStream))
	}))
	defer srv.Close()

	events := drainOpenAI(t, responsesTestClient(srv.URL))

	require.Equal(t, llm.EventRetryWait, events[0].Type)
	assert.Equal(t, 4*time.Second, events[0].Retry.Delay)
	assert.Equal(t, llm.EventComplete, events[len(events)-1].Type)
	assert.Equal(t, int32(2), hits.Load(), "SDK retries are off: one request per attempt")
}

func TestResponsesStream_LongRetryAfterBecomesTheUsageLimitMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"daily limit"}}`))
	}))
	defer srv.Close()

	events := drainOpenAI(t, responsesTestClient(srv.URL))
	last := events[len(events)-1]
	require.Equal(t, llm.EventError, last.Type)
	kind, payload, found := chatmarkers.Extract(last.Error.Error())
	require.True(t, found)
	assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
	reset, err := time.Parse(time.RFC3339, payload)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), reset, time.Minute)
	assert.Contains(t, last.Error.Error(), "OpenAI")
}

func TestResponsesStream_InsufficientQuotaIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"You exceeded your current quota"}}`))
	}))
	defer srv.Close()

	events := drainOpenAI(t, responsesTestClient(srv.URL))
	last := events[len(events)-1]
	require.Equal(t, llm.EventError, last.Type)
	assert.Contains(t, last.Error.Error(), "insufficient_quota", "unchanged, for IsProviderCreditExhaustion")
	_, _, found := chatmarkers.Extract(last.Error.Error())
	assert.False(t, found)
	assert.Equal(t, int32(1), hits.Load())
}

func TestOpenAIObserverSeesRateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-requests", "500")
		w.Header().Set("x-ratelimit-remaining-requests", "499")
		w.Header().Set("x-ratelimit-reset-requests", "120ms")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(openaiOKStream))
	}))
	defer srv.Close()

	var got []ratelimit.Snapshot
	ctx := ratelimit.WithObserver(context.Background(), observerFunc(func(s ratelimit.Snapshot) { got = append(got, s) }))
	c := responsesTestClient(srv.URL)
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	for range c.StreamResponse(ctx, nil, msgs, nil) {
	}
	require.Len(t, got, 1)
	assert.Equal(t, "openai", got[0].Provider)
	assert.Equal(t, ratelimit.StatusAllowed, got[0].Status)
}

type observerFunc func(ratelimit.Snapshot)

func (f observerFunc) ObserveRateLimits(_ context.Context, s ratelimit.Snapshot) { f(s) }

func TestRetryDecision_ChatPathUsesTheSharedLadder(t *testing.T) {
	c := &OpenaiClient{ladder: ratelimit.Retryer{Jitter: func(time.Duration) time.Duration { return 0 }}}
	apiErr := func(status int, h http.Header) error {
		req, _ := http.NewRequest(http.MethodPost, "http://x/chat/completions", nil)
		return &openaisdk.Error{StatusCode: status, Request: req, Response: &http.Response{StatusCode: status, Header: h}}
	}
	h := http.Header{}
	h.Set("Retry-After", "5")
	wait, retry, err := c.retryDecision(1, apiErr(429, h))
	require.NoError(t, err)
	assert.True(t, retry)
	assert.Equal(t, 5*time.Second, wait.Delay)

	h.Set("Retry-After", "3600")
	_, retry, err = c.retryDecision(1, apiErr(429, h))
	assert.False(t, retry)
	_, _, found := chatmarkers.Extract(err.Error())
	assert.True(t, found, "a >5 min wait no longer sleeps unboundedly in the driver")

	_, retry, _ = c.retryDecision(1, apiErr(400, nil))
	assert.False(t, retry)
	_, retry, _ = c.retryDecision(1, errors.New("not an api error"))
	assert.True(t, retry, "a connection error is retried")
}
