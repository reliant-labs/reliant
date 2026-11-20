// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const (
	completionBody = `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`
	chunkBody      = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
)

// openRouterServer refuses the first `refusals` requests with (status, body,
// headers), then answers, streaming when the request asked to.
func openRouterServer(t *testing.T, hits *atomic.Int32, refusals int32, status int, body string, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= refusals {
			for k, v := range hdr {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
			return
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, chunkBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, completionBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newRetryClient(srv *httptest.Server, apiModel string, sleeps *[]time.Duration, maxAttempts int) *Client {
	c := NewClient(llm.DriverOptions{ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100, Model: models.Model{ID: "test-model", APIModel: apiModel}})
	c.ladder = ratelimit.Retryer{
		MaxAttempts: maxAttempts,
		Jitter:      func(time.Duration) time.Duration { return 0 },
		Sleep:       func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
	}
	return c
}

func turn(c *Client, stream bool) (string, []llm.RetryWait, error) {
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	if !stream {
		resp, err := c.SendMessages(context.Background(), nil, msgs, nil)
		if err != nil {
			return "", nil, err
		}
		return resp.Content, nil, nil
	}
	var (
		content string
		waits   []llm.RetryWait
		err     error
	)
	for ev := range c.StreamResponse(context.Background(), nil, msgs, nil) {
		switch ev.Type {
		case llm.EventRetryWait:
			waits = append(waits, *ev.Retry)
		case llm.EventComplete:
			content = ev.Response.Content
		case llm.EventError:
			err = ev.Error
		}
	}
	return content, waits, err
}

// The two raw-HTTP paths (Anthropic models for cache control, Gemini models
// for reasoning details), each way a caller makes a turn.
var rawPaths = map[string]struct {
	model  string
	stream bool
}{
	"anthropic send":   {"anthropic/claude-sonnet-4.5", false},
	"anthropic stream": {"anthropic/claude-sonnet-4.5", true},
	"gemini send":      {"google/gemini-2.5-pro", false},
	"gemini stream":    {"google/gemini-2.5-pro", true},
}

func TestRawPaths_RetryA429AfterItsRetryAfter(t *testing.T) {
	for name, p := range rawPaths {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := openRouterServer(t, &hits, 1, http.StatusTooManyRequests, `{"error":{"code":429,"message":"Rate limit exceeded"}}`, map[string]string{"Retry-After": "4"})
			var sleeps []time.Duration
			content, waits, err := turn(newRetryClient(srv, p.model, &sleeps, 0), p.stream)
			require.NoError(t, err)
			assert.Equal(t, "pong", content)
			assert.EqualValues(t, 2, hits.Load())
			assert.Equal(t, []time.Duration{4 * time.Second}, sleeps)
			if p.stream {
				require.Len(t, waits, 1, "a stream announces its wait (the old loop slept silently)")
			}
		})
	}
}

func TestRawPaths_A400IsNotRetried(t *testing.T) {
	for name, p := range rawPaths {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := openRouterServer(t, &hits, 1, http.StatusBadRequest, `{"error":{"code":400,"message":"prompt is too long"}}`, nil)
			var sleeps []time.Duration
			_, _, err := turn(newRetryClient(srv, p.model, &sleeps, 0), p.stream)
			require.ErrorContains(t, err, "API request failed with status 400")
			assert.EqualValues(t, 1, hits.Load())
			assert.Empty(t, sleeps)
		})
	}
}

// A 408 or a 504 was outside OpenRouter's old private retry set; the shared
// one retries them like every other driver. The ladder is held to one retry,
// so exactly two requests also proves nothing else retries underneath.
func TestRawPaths_ShareTheLaddersRetryableSet(t *testing.T) {
	var hits atomic.Int32
	srv := openRouterServer(t, &hits, 100, http.StatusGatewayTimeout, `{"error":{"code":504,"message":"upstream timeout"}}`, nil)
	var sleeps []time.Duration
	_, _, err := turn(newRetryClient(srv, "anthropic/claude-sonnet-4.5", &sleeps, 1), false)
	require.Error(t, err)
	assert.EqualValues(t, 2, hits.Load())
	assert.Equal(t, []time.Duration{2 * time.Second}, sleeps)
}

// The free models' daily cap lifts at X-RateLimit-Reset, hours away: it ends
// the turn as a usage window instead of eight futile retries.
func TestDailyLimitEndsTheLadder(t *testing.T) {
	reset := time.Now().Add(5 * time.Hour).Truncate(time.Millisecond)
	body := `{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day"}}`
	for name, p := range map[string]struct {
		model  string
		stream bool
	}{
		"raw path":      {"anthropic/claude-sonnet-4.5", true},
		"openai client": {"openai/gpt-5.4", true},
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := openRouterServer(t, &hits, 1, http.StatusTooManyRequests, body,
				map[string]string{"X-RateLimit-Reset": strconv.FormatInt(reset.UnixMilli(), 10), "X-RateLimit-Remaining": "0"})
			var sleeps []time.Duration
			_, _, err := turn(newRetryClient(srv, p.model, &sleeps, 0), p.stream)
			var ule *ratelimit.UsageLimitError
			require.ErrorAs(t, err, &ule)
			assert.Equal(t, "OpenRouter", ule.Provider)
			assert.Equal(t, "daily", ule.Window)
			assert.True(t, ule.ResetAt.Equal(reset.UTC()), "reset = %s, want %s", ule.ResetAt, reset)
			assert.EqualValues(t, 1, hits.Load())
			assert.Empty(t, sleeps)
		})
	}
}

// Models with no path of their own go through the OpenAI client, which runs
// the same ladder. Its clock is not injectable from here, so the wait is 0s.
func TestOpenAIClientPath_RetriesThroughTheLadder(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := openRouterServer(t, &hits, 1, http.StatusTooManyRequests, `{"error":{"code":429,"message":"Rate limit exceeded"}}`, map[string]string{"Retry-After": "0"})
			var sleeps []time.Duration
			content, waits, err := turn(newRetryClient(srv, "openai/gpt-5.4", &sleeps, 0), stream)
			require.NoError(t, err)
			assert.Equal(t, "pong", content)
			assert.EqualValues(t, 2, hits.Load())
			if stream {
				require.Len(t, waits, 1)
			}
		})
	}
}
