// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	chatCompletionBody = `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`
	chatChunkBody      = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
)

// chatServer refuses the first `refusals` requests with status (and
// Retry-After when retryAfter is set), then answers.
func chatServer(t *testing.T, hits *atomic.Int32, refusals int32, status int, retryAfter string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= refusals {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"message":"refused %d","type":"x"}}`, status)
			return
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, chatChunkBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatCompletionBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newChatRetryClient(srv *httptest.Server, sleeps *[]time.Duration, maxAttempts int) *OpenaiClient {
	c := NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100})
	c.ladder = ratelimit.Retryer{
		MaxAttempts: maxAttempts,
		Jitter:      func(time.Duration) time.Duration { return 0 },
		Sleep:       func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
	}
	return c
}

// chatTurn runs one Chat Completions turn either way a caller makes one and
// returns its content, the waits it announced, and its error.
func chatTurn(c *OpenaiClient, stream bool) (string, []llm.RetryWait, error) {
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

func TestChat_RetriesA429AfterItsRetryAfter(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := chatServer(t, &hits, 1, http.StatusTooManyRequests, "3")
			var sleeps []time.Duration
			content, waits, err := chatTurn(newChatRetryClient(srv, &sleeps, 0), stream)
			require.NoError(t, err)
			assert.Equal(t, "pong", content)
			assert.EqualValues(t, 2, hits.Load())
			assert.Equal(t, []time.Duration{3 * time.Second}, sleeps)
			if stream {
				require.Len(t, waits, 1, "the stream announces the wait before it sleeps")
				assert.Equal(t, http.StatusTooManyRequests, waits[0].StatusCode)
			}
		})
	}
}

func TestChat_A400IsNotRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := chatServer(t, &hits, 1, http.StatusBadRequest, "")
			var sleeps []time.Duration
			_, _, err := chatTurn(newChatRetryClient(srv, &sleeps, 0), stream)
			require.ErrorContains(t, err, "refused 400")
			assert.EqualValues(t, 1, hits.Load())
			assert.Empty(t, sleeps)
		})
	}
}

// openai-go retries twice per request by default. With the ladder allowed one
// retry, a server that always fails must see exactly two requests: one more
// would be the SDK retrying inside an attempt.
func TestChat_SDKRetriesAreOff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := chatServer(t, &hits, 100, http.StatusServiceUnavailable, "")
			var sleeps []time.Duration
			_, _, err := chatTurn(newChatRetryClient(srv, &sleeps, 1), stream)
			require.Error(t, err)
			assert.EqualValues(t, 2, hits.Load())
		})
	}
}
