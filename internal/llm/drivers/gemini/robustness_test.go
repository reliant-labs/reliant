// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers/geminiwire"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// cannedResponse is one reply of the fake Gemini server: an SSE body, or an
// error status with a Google error body.
type cannedResponse struct {
	status int
	body   string
}

// fakeGemini serves each request the next canned response and records every
// request body.
type fakeGemini struct {
	mu        sync.Mutex
	responses []cannedResponse
	requests  []string
}

func newFakeGemini(t *testing.T, responses ...cannedResponse) (*fakeGemini, *GeminiClient) {
	t.Helper()
	f := &fakeGemini{responses: responses}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, string(body))
		f.mu.Unlock()
		if n >= len(f.responses) {
			t.Errorf("unexpected request %d", n+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := f.responses[n]
		if resp.status != 0 && resp.status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.status)
			_, _ = io.WriteString(w, resp.body)
			return
		}
		if r.URL.Query().Get("alt") != "sse" {
			// The unary generateContent answers with the bare JSON.
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, resp.body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", resp.body)
	}))
	t.Cleanup(srv.Close)

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      "test-key",
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	require.NoError(t, err)
	return f, &GeminiClient{
		options: llm.DriverOptions{Model: models.Model{APIModel: "gemini-3-pro-preview"}, MaxTokens: 1000},
		client:  client,
	}
}

func (f *fakeGemini) requestBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func streamAll(c *GeminiClient, messages []message.Message) []llm.DriverEvent {
	var events []llm.DriverEvent
	for ev := range c.StreamResponse(context.Background(), nil, messages, nil) {
		events = append(events, ev)
	}
	return events
}

func firstError(events []llm.DriverEvent) error {
	for _, ev := range events {
		if ev.Type == llm.EventError {
			return ev.Error
		}
	}
	return nil
}

func lastComplete(events []llm.DriverEvent) *llm.DriverResponse {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == llm.EventComplete {
			return events[i].Response
		}
	}
	return nil
}

func userMsg(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

// MALFORMED_FUNCTION_CALL used to complete the turn with a tool_use_error
// finish, which the agent loop reads as an error stop: the turn ended. It now
// ends the stream with an error CallLLM recognises and retries once, with the
// usage the request billed still delivered after it.
func TestStream_MalformedFunctionCallIsARetryableError(t *testing.T) {
	t.Parallel()
	_, c := newFakeGemini(t, cannedResponse{body: `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: print(default_api.bash(command=\"ls\""}],` +
		`"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0,"totalTokenCount":100}}`})

	events := streamAll(c, []message.Message{userMsg("list files")})
	err := firstError(events)
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrMalformedModelOutput)
	// The SDK's Gemini API backend drops finishMessage, so only the reason
	// reaches the error here.
	assert.Contains(t, err.Error(), "Gemini stopped with MALFORMED_FUNCTION_CALL")
	resp := lastComplete(events)
	require.NotNil(t, resp, "the billed usage still follows the error")
	assert.Equal(t, int64(100), resp.Usage.InputTokens)
}

// A candidate that stopped normally is untouched.
func TestStream_StopCompletes(t *testing.T) {
	t.Parallel()
	_, c := newFakeGemini(t, cannedResponse{body: `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":10000,"cachedContentTokenCount":8000,"candidatesTokenCount":5,"thoughtsTokenCount":20,"totalTokenCount":10025}}`})

	events := streamAll(c, []message.Message{userMsg("hi")})
	require.NoError(t, firstError(events))
	resp := lastComplete(events)
	require.NotNil(t, resp)
	assert.Equal(t, "hi", resp.Content)
	assert.Equal(t, message.FinishReasonEndTurn, resp.FinishReason)
	assert.Equal(t, llm.TokenUsage{
		TokenCount: 10025, InputTokens: 2000, OutputTokens: 25, ReasoningTokens: 20,
		CachedInputTokens: 8000, CacheReadInputTokens: 8000,
	}, resp.Usage, "cached tokens are reported and not counted twice; thoughts are output")
}

func signedHistory() []message.Message {
	sig := base64.StdEncoding.EncodeToString([]byte("signature-from-another-model"))
	call := message.ToolCall{ID: "call_1", Name: "bash", Input: `{"command":"ls"}`, ThoughtSignature: sig}
	return []message.Message{
		userMsg("list files"),
		{Role: message.Assistant, Parts: []message.ContentPart{call}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_1", Name: "bash", Content: "a.txt"}}},
	}
}

// A signature Gemini rejects is replaced by the documented stand-in and the
// request sent once more, instead of failing every later turn of the chat.
func TestStream_RejectedSignatureIsRetriedWithStandIns(t *testing.T) {
	t.Parallel()
	f, c := newFakeGemini(t,
		cannedResponse{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`},
		cannedResponse{body: `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`},
	)

	events := streamAll(c, signedHistory())
	require.NoError(t, firstError(events))
	resp := lastComplete(events)
	require.NotNil(t, resp)
	assert.Equal(t, "done", resp.Content)

	bodies := f.requestBodies()
	require.Len(t, bodies, 2, "one retry")
	original := base64.StdEncoding.EncodeToString([]byte("signature-from-another-model"))
	assert.Contains(t, bodies[0], original)
	assert.NotContains(t, bodies[1], original)
	assert.Contains(t, bodies[1], `"thoughtSignature":"skip/thought/signature/validator"`)
}

// A request still rejected with stand-ins is not a signature problem, and is
// not retried again.
func TestStream_SignatureRecoveryIsBoundedToOneRetry(t *testing.T) {
	t.Parallel()
	rejected := cannedResponse{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`}
	f, c := newFakeGemini(t, rejected, rejected)

	events := streamAll(c, signedHistory())
	err := firstError(events)
	require.Error(t, err)
	assert.True(t, geminiwire.IsThoughtSignatureRejection(err))
	assert.Len(t, f.requestBodies(), 2)
}

// Unsigned calls in history (made by another model) get the stand-in on a
// model that validates signatures, so the request is not rejected at all.
func TestStream_UnsignedCallsGetTheStandIn(t *testing.T) {
	t.Parallel()
	f, c := newFakeGemini(t, cannedResponse{body: `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`})
	history := signedHistory()
	call := history[1].Parts[0].(message.ToolCall)
	call.ThoughtSignature = ""
	history[1].Parts[0] = call

	require.NoError(t, firstError(streamAll(c, history)))
	bodies := f.requestBodies()
	require.Len(t, bodies, 1)
	assert.Equal(t, 1, strings.Count(bodies[0], `"thoughtSignature":"skip/thought/signature/validator"`))
}

// SendMessages (the unary path) shares both behaviours.
func TestSendMessages_MalformedAndSignatureRecovery(t *testing.T) {
	t.Parallel()

	_, c := newFakeGemini(t, cannedResponse{body: `{"candidates":[{"finishReason":"UNEXPECTED_TOOL_CALL"}]}`})
	_, err := c.SendMessages(context.Background(), nil, []message.Message{userMsg("x")}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, drivererrors.ErrMalformedModelOutput))

	f, c := newFakeGemini(t,
		cannedResponse{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"Function call is missing a thought_signature in functionCall parts.","status":"INVALID_ARGUMENT"}}`},
		cannedResponse{body: `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`},
	)
	resp, err := c.SendMessages(context.Background(), nil, signedHistory(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Len(t, f.requestBodies(), 2)
}

// The tool schema goes through the shared converter, so a nullable optional
// field keeps its type instead of reaching Gemini untyped.
func TestConvertTools_UsesSharedSchemaConverter(t *testing.T) {
	t.Parallel()
	tool := tools.NewSchemaOnlyTool("mcp__x", "x", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{"type": []any{"integer", "null"}},
		},
		"required": []any{"limit", "ghost"},
	})
	decls := newTestClient().convertTools([]tools.Tool{tool})
	require.Len(t, decls, 1)
	params := decls[0].FunctionDeclarations[0].Parameters
	require.NotNil(t, params)
	assert.Equal(t, genai.TypeObject, params.Type)
	assert.Equal(t, &genai.Schema{Type: genai.TypeInteger, Nullable: genai.Ptr(true)}, params.Properties["limit"])
	assert.Equal(t, []string{"limit"}, params.Required)
}
