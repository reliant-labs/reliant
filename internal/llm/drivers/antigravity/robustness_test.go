// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers/geminiwire"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// MALFORMED_FUNCTION_CALL ends the stream with an error CallLLM retries once,
// rather than completing the turn with a tool_use_error finish, and the
// usage the request billed still follows it.
func TestConsumeStream_MalformedFunctionCallIsARetryableError(t *testing.T) {
	t.Parallel()
	events := collect(t, `data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: call:reliant__bash{"}],"usageMetadata":{"promptTokenCount":42,"totalTokenCount":42}}}`+"\n\n")

	var err error
	var billed *llm.DriverResponse
	for _, ev := range events {
		switch ev.Type {
		case llm.EventError:
			err = ev.Error
		case llm.EventComplete:
			billed = ev.Response
		}
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrMalformedModelOutput)
	assert.Equal(t, "Gemini stopped with MALFORMED_FUNCTION_CALL: Malformed function call: call:reliant__bash{", err.Error())
	require.NotNil(t, billed)
	assert.Equal(t, int64(42), billed.Usage.InputTokens)
	assert.Empty(t, billed.Content, "the usage-only event is not an answer")
}

// A malformed stop that still produced well-formed calls runs them.
func TestConsumeStream_MalformedStopWithCallsCompletes(t *testing.T) {
	t.Parallel()
	events := collect(t, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"reliant__view","args":{}},"thoughtSignature":"c2ln"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`+"\n\n")
	resp := completeEvent(t, events)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, message.FinishReasonToolUse, resp.FinishReason)
}

// The cached prefix is inside promptTokenCount, so input is the remainder.
func TestUsage_CachedPrefixIsNotCountedTwice(t *testing.T) {
	t.Parallel()
	resp := &generateResp{UsageMetadata: &usageMetadata{
		PromptTokenCount: 1000, CachedContentTokenCount: 900, CandidatesTokenCount: 5, ThoughtsTokenCount: 10, TotalTokenCount: 1015,
	}}
	got := usage(resp)
	assert.Equal(t, llm.TokenUsage{
		TokenCount: 1015, InputTokens: 100, OutputTokens: 15, ReasoningTokens: 10,
		CachedInputTokens: 900, CacheReadInputTokens: 900,
	}, got)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeTransport answers each request with the next (status, body), recording
// the request bodies.
func fakeTransport(t *testing.T, replies ...[2]string) (*http.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(b))
		mu.Unlock()
		if n >= len(replies) {
			t.Errorf("unexpected request %d", n+1)
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		status := http.StatusOK
		if replies[n][0] != "200" {
			status = http.StatusBadRequest
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(replies[n][1])), Header: http.Header{}, Request: r}, nil
	})}
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func signedToolTurn(sig string) []message.Message {
	return []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "plan", Signature: sig},
			message.ToolCall{ID: "x", Name: "view", Input: `{}`, ThoughtSignature: sig},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "x", Name: "view", Content: "ok"}}},
	}
}

// A signature the server rejects is replaced by the stand-in, and the request
// sent once more.
func TestStreamResponse_RejectedSignatureIsRetriedWithStandIns(t *testing.T) {
	t.Parallel()
	httpClient, bodies := fakeTransport(t,
		[2]string{"400", `{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`},
		[2]string{"200", `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}}` + "\n\n"},
	)
	c := &Client{httpClient: httpClient}
	c.options.Model.APIModel = "gemini-3.8-flash"

	var resp *llm.DriverResponse
	for ev := range c.StreamResponse(context.Background(), nil, signedToolTurn("stale-signature"), nil) {
		require.NotEqual(t, llm.EventError, ev.Type, "%v", ev.Error)
		if ev.Type == llm.EventComplete {
			resp = ev.Response
		}
	}
	require.NotNil(t, resp)
	assert.Equal(t, "done", resp.Content)

	got := bodies()
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "stale-signature")
	assert.NotContains(t, got[1], "stale-signature")
	assert.NotContains(t, got[1], `"thought":true`, "the replayed reasoning went with its signature")
	assert.Equal(t, 1, strings.Count(got[1], `"thoughtSignature":"`+geminiwire.SkipThoughtSignatureValidator+`"`))
}

func TestStreamResponse_SignatureRecoveryIsBoundedToOneRetry(t *testing.T) {
	t.Parallel()
	rejected := [2]string{"400", `{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`}
	httpClient, bodies := fakeTransport(t, rejected, rejected)
	c := &Client{httpClient: httpClient}
	c.options.Model.APIModel = "gemini-3.8-flash"

	var err error
	for ev := range c.StreamResponse(context.Background(), nil, signedToolTurn("stale-signature"), nil) {
		if ev.Type == llm.EventError {
			err = ev.Error
		}
	}
	require.Error(t, err)
	assert.True(t, geminiwire.IsThoughtSignatureRejection(err))
	assert.Len(t, bodies(), 2)
}

// The declared schema keeps what the old hand-kept subset dropped.
func TestConvertTools_SchemaIsFaithful(t *testing.T) {
	t.Parallel()
	tool := tools.NewSchemaOnlyTool("t", "t", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opt":  map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
			"when": map[string]any{"type": "string", "format": "date-time"},
		},
	})
	params := convertTools([]tools.Tool{tool})[0].FunctionDeclarations[0].Parameters
	require.NotNil(t, params.Properties["opt"].Nullable)
	assert.True(t, *params.Properties["opt"].Nullable)
	assert.Equal(t, "date-time", params.Properties["when"].Format)
}
