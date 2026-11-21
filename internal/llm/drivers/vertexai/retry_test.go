// Copyright (c) 2025 Reliant Labs
package vertexai

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

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// statusServer answers each request with the next canned (status, body); a
// 200 on a streaming request goes out as one SSE event.
func statusServer(t *testing.T, hits *atomic.Int32, replies ...[2]any) *httptest.Server {
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

func recordingLadder(sleeps *[]time.Duration) ratelimit.Retryer {
	return ratelimit.Retryer{
		Jitter: func(time.Duration) time.Duration { return 0 },
		Sleep:  func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
	}
}

const vertexRateLimited = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded for aiplatform.googleapis.com/generate_content_requests_per_minute_per_project_per_base_model","details":[
	{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"9s"}]}}`

func TestGemini_RetriesA429AfterTheRetryInfoWait(t *testing.T) {
	user := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	for name, run := range map[string]func(c *VertexAIClient) (string, []llm.RetryWait, error){
		"send": func(c *VertexAIClient) (string, []llm.RetryWait, error) {
			resp, err := c.SendMessages(context.Background(), nil, user, nil)
			if err != nil {
				return "", nil, err
			}
			return resp.Content, nil, nil
		},
		"stream": func(c *VertexAIClient) (string, []llm.RetryWait, error) {
			var waits []llm.RetryWait
			var content string
			for ev := range c.StreamResponse(context.Background(), nil, user, nil) {
				switch ev.Type {
				case llm.EventRetryWait:
					waits = append(waits, *ev.Retry)
				case llm.EventComplete:
					content = ev.Response.Content
				case llm.EventError:
					return "", waits, ev.Error
				}
			}
			return content, waits, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := statusServer(t, &hits, [2]any{429, vertexRateLimited},
				[2]any{200, `{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}]}`})
			c := newStreamingGeminiClient(t, srv)
			var sleeps []time.Duration
			c.ladder = recordingLadder(&sleeps)

			content, waits, err := run(c)
			require.NoError(t, err)
			assert.Equal(t, "pong", content)
			assert.EqualValues(t, 2, hits.Load(), "one retry, and genai retries nothing of its own")
			assert.Equal(t, []time.Duration{9 * time.Second}, sleeps)
			if name == "stream" {
				require.Len(t, waits, 1, "the stream announces its wait")
			}
		})
	}
}

func TestGemini_A400IsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, &hits, [2]any{400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad"}}`})
	c := newStreamingGeminiClient(t, srv)
	var sleeps []time.Duration
	c.ladder = recordingLadder(&sleeps)
	_, err := c.SendMessages(context.Background(), nil, []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}, nil)
	require.Error(t, err)
	assert.EqualValues(t, 1, hits.Load())
	assert.Empty(t, sleeps)
}

func TestClaude_RetriesA429ThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, &hits,
		[2]any{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`},
		[2]any{200, `{"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn"}`})
	c := &VertexAIClient{}
	var sleeps []time.Duration
	c.ladder = recordingLadder(&sleeps)

	body, err := c.makeClaudeRequest(context.Background(), srv.URL, &claudeRequest{}, "tok")
	require.NoError(t, err)
	assert.Contains(t, string(body), "pong")
	assert.EqualValues(t, 2, hits.Load())
	assert.Equal(t, []time.Duration{2 * time.Second}, sleeps)
}

func TestClaude_A400IsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, &hits, [2]any{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`})
	c := &VertexAIClient{}
	var sleeps []time.Duration
	c.ladder = recordingLadder(&sleeps)

	_, err := c.makeClaudeRequest(context.Background(), srv.URL, &claudeRequest{}, "tok")
	require.ErrorContains(t, err, "API error (status 400)")
	require.ErrorContains(t, err, "prompt is too long")
	assert.EqualValues(t, 1, hits.Load())
	assert.Empty(t, sleeps)
}

func TestClaude_AUsageWindowEndsAtOnce(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, &hits, [2]any{429, `{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your usage limit"}}`})
	c := &VertexAIClient{}
	var sleeps []time.Duration
	c.ladder = recordingLadder(&sleeps)

	_, err := c.makeClaudeRequest(context.Background(), srv.URL, &claudeRequest{}, "tok")
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, err, &ule)
	assert.EqualValues(t, 1, hits.Load())
}
