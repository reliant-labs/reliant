// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Gemini states its wait in the error's google.rpc.RetryInfo, never in a
// Retry-After header. The old string-matching ladder ignored it and slept its
// own 2s doubling; the shared ladder sleeps what the provider asked for.
const rateLimited = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":[
	{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7s"}]}}`

const answer = `{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}]}`

// geminiServer answers each request with the next canned (status, body); a
// 200 on a streaming request goes out as one SSE event.
func geminiServer(t *testing.T, hits *atomic.Int32, replies ...[2]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1)) - 1
		require.Less(t, n, len(replies), "unexpected extra request")
		status, body := replies[n][0].(int), replies[n][1].(string)
		if status == http.StatusOK && r.URL.Query().Get("alt") == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newRetryTestClient(t *testing.T, srv *httptest.Server, sleeps *[]time.Duration) *GeminiClient {
	t.Helper()
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      "test-key",
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	require.NoError(t, err)
	return &GeminiClient{
		options: llm.DriverOptions{Model: models.Model{APIModel: "gemini-2.5-pro"}, MaxTokens: 100},
		client:  client,
		ladder: ratelimit.Retryer{
			Jitter: func(time.Duration) time.Duration { return 0 },
			Sleep:  func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
		},
	}
}

func hello() []message.Message {
	return []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "ping"}}}}
}

func TestSendMessages_RetriesA429AfterTheRetryInfoWait(t *testing.T) {
	var hits atomic.Int32
	srv := geminiServer(t, &hits, [2]any{429, rateLimited}, [2]any{200, answer})
	var sleeps []time.Duration
	resp, err := newRetryTestClient(t, srv, &sleeps).SendMessages(context.Background(), nil, hello(), nil)
	require.NoError(t, err)
	assert.Equal(t, "pong", resp.Content)
	assert.EqualValues(t, 2, hits.Load(), "one retry, and genai retries nothing of its own")
	assert.Equal(t, []time.Duration{7 * time.Second}, sleeps)
}

func TestSendMessages_A400IsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := geminiServer(t, &hits, [2]any{400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count exceeds the maximum number of tokens allowed."}}`})
	var sleeps []time.Duration
	_, err := newRetryTestClient(t, srv, &sleeps).SendMessages(context.Background(), nil, hello(), nil)
	require.ErrorContains(t, err, "exceeds the maximum number of tokens")
	assert.EqualValues(t, 1, hits.Load())
	assert.Empty(t, sleeps)
}

func TestStreamResponse_AnnouncesTheWaitThenRetries(t *testing.T) {
	var hits atomic.Int32
	srv := geminiServer(t, &hits, [2]any{503, `{"error":{"code":503,"status":"UNAVAILABLE","message":"The model is overloaded."}}`}, [2]any{200, answer})
	var sleeps []time.Duration
	c := newRetryTestClient(t, srv, &sleeps)

	var waits []llm.RetryWait
	var final *llm.DriverResponse
	for ev := range c.StreamResponse(context.Background(), nil, hello(), nil) {
		switch ev.Type {
		case llm.EventRetryWait:
			waits = append(waits, *ev.Retry)
		case llm.EventComplete:
			final = ev.Response
		case llm.EventError:
			require.NoError(t, ev.Error)
		}
	}
	require.NotNil(t, final)
	assert.Equal(t, "pong", final.Content)
	assert.EqualValues(t, 2, hits.Load())
	require.Len(t, waits, 1)
	assert.Equal(t, http.StatusServiceUnavailable, waits[0].StatusCode)
	assert.Equal(t, []time.Duration{2 * time.Second}, sleeps, "no RetryInfo: the ladder's first rung")
}

func TestStreamResponse_ADailyQuotaEndsAtOnce(t *testing.T) {
	var hits atomic.Int32
	srv := geminiServer(t, &hits, [2]any{429, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded","details":[
		{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"250"}]},
		{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"20s"}]}}`})
	var sleeps []time.Duration
	var gotErr error
	for ev := range newRetryTestClient(t, srv, &sleeps).StreamResponse(context.Background(), nil, hello(), nil) {
		if ev.Type == llm.EventError {
			gotErr = ev.Error
		}
	}
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, gotErr, &ule)
	assert.EqualValues(t, 1, hits.Load())
	assert.Empty(t, sleeps)
}
