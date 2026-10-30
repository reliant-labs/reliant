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

// A Chat Completions stream that reported usage and then failed is retried by
// the driver, which drops that attempt's accumulator. The wait it announces
// carries what the failed request was billed, so the usage ledger can count it.
func TestChatStream_RetryWaitCarriesTheFailedAttemptsUsage(t *testing.T) {
	const chunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o",%s}`
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620}`)
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream reset\",\"type\":\"server_error\"}}\n\n")
			return
		}
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]`)
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":7,"total_tokens":47}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100})
	c.ladder = ratelimit.Retryer{MaxBackoff: time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }}
	events := drainOpenAI(t, c)

	require.EqualValues(t, 2, hits.Load())
	var waits []llm.RetryWait
	for _, ev := range events {
		if ev.Type == llm.EventRetryWait {
			waits = append(waits, *ev.Retry)
		}
	}
	require.Len(t, waits, 1)
	require.NotNil(t, waits[0].Usage, "the failed attempt reported usage")
	assert.Equal(t, [2]int64{500, 120}, [2]int64{waits[0].Usage.InputTokens, waits[0].Usage.OutputTokens})
	last := events[len(events)-1]
	require.Equal(t, llm.EventComplete, last.Type)
	assert.EqualValues(t, 40, last.Response.Usage.InputTokens, "the answer reports only its own request")
}

// A request that failed before streaming any usage announces no usage.
func TestFailedAttemptUsage_NilWhenNothingWasReported(t *testing.T) {
	assert.Nil(t, llm.FailedAttemptUsage(llm.TokenUsage{}))
	assert.NotNil(t, llm.FailedAttemptUsage(llm.TokenUsage{OutputTokens: 1}))
}
