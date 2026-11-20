// Copyright (c) 2025 Reliant Labs
package reliant

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

// gatewayServer refuses the first `refusals` requests with (status, body,
// Retry-After), then answers, streaming when the request asked to.
func gatewayServer(t *testing.T, hits *atomic.Int32, refusals int32, status int, body, retryAfter string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= refusals {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
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
			_, _ = fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newLadderTestClient(srv *httptest.Server, sleeps *[]time.Duration, maxAttempts int) *ReliantClient {
	c := NewClient(llm.DriverOptions{ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100, Model: models.Model{ID: "m", APIModel: "m"}})
	c.ladder = ratelimit.Retryer{
		MaxAttempts: maxAttempts,
		Jitter:      func(time.Duration) time.Duration { return 0 },
		Sleep:       func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
	}
	return c
}

func gatewayTurn(c *ReliantClient, stream bool) (string, []llm.RetryWait, error) {
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
			if ev.Response.Content != "" {
				content = ev.Response.Content
			}
		case llm.EventError:
			err = ev.Error
		}
	}
	return content, waits, err
}

const gateway429 = `{"error":{"message":"Rate limited","type":"rate_limit_error","code":"429"}}`

func TestGateway_RetriesA429AfterItsRetryAfter(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := gatewayServer(t, &hits, 1, http.StatusTooManyRequests, gateway429, "3")
			var sleeps []time.Duration
			content, waits, err := gatewayTurn(newLadderTestClient(srv, &sleeps, 0), stream)
			require.NoError(t, err)
			assert.Equal(t, "pong", content)
			assert.EqualValues(t, 2, hits.Load())
			assert.Equal(t, []time.Duration{3 * time.Second}, sleeps)
			if stream {
				require.Len(t, waits, 1)
				assert.Equal(t, "http_429", waits[0].Reason)
			}
		})
	}
}

// The gateway's own classification still names the wait and still decides
// what retries: a 500 is retried only when its text says it is transient.
func TestGateway_KeepsItsClassification(t *testing.T) {
	var hits atomic.Int32
	srv := gatewayServer(t, &hits, 1, http.StatusInternalServerError, `{"error":{"message":"upstream overloaded, try again","type":"api_error"}}`, "")
	var sleeps []time.Duration
	_, waits, err := gatewayTurn(newLadderTestClient(srv, &sleeps, 0), true)
	require.NoError(t, err)
	require.Len(t, waits, 1)
	assert.Equal(t, "transient_upstream_500", waits[0].Reason)

	hits.Store(0)
	srv = gatewayServer(t, &hits, 1, http.StatusInternalServerError, `{"error":{"message":"tool schema rejected","type":"api_error"}}`, "")
	_, _, err = gatewayTurn(newLadderTestClient(srv, &sleeps, 0), true)
	require.Error(t, err)
	assert.EqualValues(t, 1, hits.Load(), "a 500 the gateway calls permanent is not retried")
}

func TestGateway_A400IsNotRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := gatewayServer(t, &hits, 1, http.StatusBadRequest, `{"error":{"message":"prompt is too long","type":"invalid_request_error"}}`, "")
			var sleeps []time.Duration
			_, _, err := gatewayTurn(newLadderTestClient(srv, &sleeps, 0), stream)
			require.ErrorContains(t, err, "prompt is too long")
			assert.EqualValues(t, 1, hits.Load())
			assert.Empty(t, sleeps)
		})
	}
}

// openai-go retried twice inside every attempt of this driver's old loop. With
// the ladder held to one retry, a gateway that always fails sees exactly two
// requests.
func TestGateway_SDKRetriesAreOff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := gatewayServer(t, &hits, 100, http.StatusServiceUnavailable, `{"error":{"message":"Service unavailable","type":"api_error"}}`, "")
			var sleeps []time.Duration
			_, _, err := gatewayTurn(newLadderTestClient(srv, &sleeps, 1), stream)
			require.Error(t, err)
			assert.EqualValues(t, 2, hits.Load())
		})
	}
}

// A Retry-After past the ladder's cap is a usage limit, not a sleep: the old
// loop slept whatever the gateway asked for.
func TestGateway_LongRetryAfterIsAUsageLimit(t *testing.T) {
	var hits atomic.Int32
	srv := gatewayServer(t, &hits, 1, http.StatusTooManyRequests, gateway429, "86400")
	var sleeps []time.Duration
	_, _, err := gatewayTurn(newLadderTestClient(srv, &sleeps, 0), true)
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, err, &ule)
	assert.EqualValues(t, 1, hits.Load())
	assert.Empty(t, sleeps)
}
