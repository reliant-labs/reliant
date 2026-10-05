// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive the Claude Code driver's PUBLIC StreamResponse, not
// baseClient directly. The retry ladder (stream_retry.go) was added to
// baseClient's stream loop while ClaudeCodeClient carried its own copy of that
// loop, so the subscription driver — the one that actually hits usage limits —
// never got it: a 429 with a multi-hour Retry-After slept inside the SDK until
// CallLLM's progress guard cut the turn as "llm stream progress timeout".

// newTestClaudeCodeClient is the real Claude Code driver with only its SDK
// client swapped for one that dials url. Everything the stream path reads off
// the driver comes from NewClaudeCodeClient.
func newTestClaudeCodeClient(url string) *ClaudeCodeClient {
	c := NewClaudeCodeClient(opus55Opts())
	c.client = llm.NewAnthropicSDKClient(option.WithBaseURL(url), option.WithAPIKey("k"))
	return c
}

func userTurn() []message.Message {
	return []message.Message{{ID: "u-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
}

func TestClaudeCodeStreamSurfacesUsageLimitInsteadOfSleepingInTheSDK(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// A five-hour subscription window with 4.5h left: far past what the
		// driver will sleep inside one request.
		w.Header().Set("Retry-After", "16200")
		w.Header().Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"limit"}}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	events := collect(t, newTestClaudeCodeClient(srv.URL).StreamResponse(ctx, nil, userTurn(), nil))

	assert.Less(t, time.Since(start), 5*time.Second, "a usage limit must fail the request, not wait it out")
	assert.Equal(t, int32(1), hits.Load(), "the SDK must not retry underneath the driver")
	var limitErr error
	for _, ev := range events {
		if ev.Type == llm.EventError {
			limitErr = ev.Error
		}
	}
	require.Error(t, limitErr, "the usage limit must surface as an error event")
	var tooLong *RetryAfterTooLongError
	require.ErrorAs(t, limitErr, &tooLong)
	kind, _, found := chatmarkers.Extract(limitErr.Error())
	require.True(t, found, "the chat routes on the marker; without it the turn is retried as a generic error")
	assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
	assert.Contains(t, chatmarkers.ProviderUsageLimitSummary(limitErr.Error()), "5-hour window")
}

const toolUseSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__reliant__view","input":{}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"a.go\"}"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// A short 429 is retried by the driver with the wait published first, and the
// MCP-style prefix the driver presents tools under is still stripped from
// every tool name the model returns — streamed and final alike.
func TestClaudeCodeStreamPublishesRetryWaitAndStripsToolPrefix(t *testing.T) {
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
		_, _ = w.Write([]byte(toolUseSSE))
	}))
	defer srv.Close()

	events := collect(t, newTestClaudeCodeClient(srv.URL).StreamResponse(context.Background(), nil, userTurn(), nil))

	require.NotEmpty(t, events)
	require.Equal(t, llm.EventRetryWait, events[0].Type, "the wait must be published before it is taken")
	assert.Equal(t, 429, events[0].Retry.StatusCode)
	assert.Equal(t, int32(2), hits.Load(), "one 429 then one success — any more means the SDK is retrying underneath")

	var sawStart bool
	var final *llm.DriverResponse
	for _, ev := range events {
		switch ev.Type {
		case llm.EventError:
			t.Fatalf("a retried 429 must not surface as an error: %v", ev.Error)
		case llm.EventToolUseStart:
			sawStart = true
			assert.Equal(t, "view", ev.ToolCall.Name)
		case llm.EventComplete:
			final = ev.Response
		}
	}
	assert.True(t, sawStart)
	require.NotNil(t, final)
	require.Len(t, final.ToolCalls, 1)
	assert.Equal(t, "view", final.ToolCalls[0].Name)
	assert.JSONEq(t, `{"file_path":"a.go"}`, final.ToolCalls[0].Input)
}
