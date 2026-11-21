// Copyright (c) 2025 Reliant Labs
package openai

import (
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
)

// A Chat Completions stream that already delivered a chunk is not retried by
// the driver: its events reached the consumer, and a second request would
// replay them. The failure goes out with what the request was billed, and
// CallLLM's own retry takes it from there.
func TestChatStream_FailureAfterTheFirstChunkIsNotRetried(t *testing.T) {
	const chunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o",%s}`
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"par"}}]`)
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620}`)
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream reset\",\"type\":\"server_error\"}}\n\n")
	}))
	defer srv.Close()

	c := NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100})
	c.ladder = ratelimit.Retryer{MaxBackoff: time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }}
	events := drainOpenAI(t, c)

	require.EqualValues(t, 1, hits.Load(), "a stream that already delivered content must not be re-requested")
	var deltas int
	for _, ev := range events {
		require.NotEqual(t, llm.EventRetryWait, ev.Type, "%+v", events)
		if ev.Type == llm.EventContentDelta {
			deltas++
		}
	}
	assert.Equal(t, 1, deltas, "the delta is delivered once")
	errEv, last := events[len(events)-2], events[len(events)-1]
	require.Equal(t, llm.EventError, errEv.Type, "%+v", events)
	assert.ErrorContains(t, errEv.Error, "upstream reset")
	require.Equal(t, llm.EventComplete, last.Type)
	assert.Equal(t, [2]int64{500, 120}, [2]int64{last.Response.Usage.InputTokens, last.Response.Usage.OutputTokens})
}

// The request that ends the stream can fail too: the ladder runs out, or the
// failure is not retryable. Its accumulator is dropped as well, so what it was
// billed goes out after the error, as a final event holding only that usage;
// the error comes first, so nobody reads it as an answer.
func TestChatStream_TerminalFailureStillReportsItsUsage(t *testing.T) {
	const chunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o",%s}`
	for name, tc := range map[string]struct {
		body      func(w http.ResponseWriter)
		wantError string
	}{
		"ladder exhausted after usage": {
			body: func(w http.ResponseWriter) {
				fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"par"}}]`)
				fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620}`)
				fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream reset\",\"type\":\"server_error\"}}\n\n")
			},
			wantError: "upstream reset",
		},
		"usage but no choices": {
			body: func(w http.ResponseWriter) {
				fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620}`)
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
			wantError: "empty response",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				tc.body(w)
			}))
			defer srv.Close()

			c := NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100})
			c.ladder = ratelimit.Retryer{MaxAttempts: 1, MaxBackoff: time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }}
			events := drainOpenAI(t, c)

			require.GreaterOrEqual(t, len(events), 2, "%+v", events)
			errEv, last := events[len(events)-2], events[len(events)-1]
			require.Equal(t, llm.EventError, errEv.Type, "the error comes before the usage: %+v", events)
			assert.ErrorContains(t, errEv.Error, tc.wantError)
			require.Equal(t, llm.EventComplete, last.Type, "the failed request's usage follows the error: %+v", events)
			require.NotNil(t, last.Response)
			assert.Equal(t, [2]int64{500, 120}, [2]int64{last.Response.Usage.InputTokens, last.Response.Usage.OutputTokens})
			assert.Empty(t, last.Response.Content, "the usage-only event carries no answer")
			assert.Empty(t, last.Response.FinishReason)
		})
	}
}

// A request that failed before streaming any usage announces no usage.
func TestFailedAttemptUsage_NilWhenNothingWasReported(t *testing.T) {
	assert.Nil(t, llm.FailedAttemptUsage(llm.TokenUsage{}))
	assert.NotNil(t, llm.FailedAttemptUsage(llm.TokenUsage{OutputTokens: 1}))
}
