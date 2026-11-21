// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// recordingGemini serves each request the next canned (status, body) and
// records the request bodies. A 200 body is sent as one SSE event.
func recordingGemini(t *testing.T, model string, replies ...[2]string) (*VertexAIClient, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(b))
		mu.Unlock()
		if n >= len(replies) {
			t.Errorf("unexpected request %d", n+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if replies[n][0] != "200" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, replies[n][1])
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", replies[n][1])
	}))
	t.Cleanup(srv.Close)
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      "test-key",
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	require.NoError(t, err)
	c := &VertexAIClient{
		options:      llm.DriverOptions{Model: models.Model{APIModel: model}},
		provider:     providerGemini,
		geminiClient: client,
	}
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func streamVertex(c *VertexAIClient, msgs []message.Message) (*llm.DriverResponse, error) {
	var err error
	var resp *llm.DriverResponse
	for ev := range c.StreamResponse(context.Background(), nil, msgs, nil) {
		switch ev.Type {
		case llm.EventError:
			if err == nil {
				err = ev.Error
			}
		case llm.EventComplete:
			resp = ev.Response
		}
	}
	return resp, err
}

func vertexUser(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

// The malformed-call error keeps failing the stream, as Vertex always did, but
// now as an error CallLLM recognises and retries once.
func TestVertexGemini_MalformedFunctionCallIsRetryable(t *testing.T) {
	t.Parallel()
	c, _ := recordingGemini(t, "gemini-3-pro-preview", [2]string{"200", `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"bad call"}]}`})
	_, err := streamVertex(c, []message.Message{vertexUser("x")})
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrMalformedModelOutput)
	// The SDK's Gemini API backend (this fake) drops finishMessage; Vertex keeps it.
	assert.Contains(t, err.Error(), "Gemini stopped with MALFORMED_FUNCTION_CALL")

	_, sendErr := c.convertGeminiResponse(&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonSafety}}})
	require.Error(t, sendErr)
	assert.NotErrorIs(t, sendErr, drivererrors.ErrMalformedModelOutput, "a content filter is not retried")
}

// Vertex dropped thought signatures in both directions, so every Gemini 3
// request that replayed a call of the current turn was rejected. The
// signature is now captured off the call and replayed on it.
func TestVertexGemini_ThoughtSignatureRoundTrip(t *testing.T) {
	t.Parallel()
	sig := []byte("vertex-issued-signature")
	c, bodies := recordingGemini(t, "gemini-3-pro-preview",
		[2]string{"200", fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}},"thoughtSignature":%q}]},"finishReason":"STOP"}]}`,
			base64.StdEncoding.EncodeToString(sig))},
		[2]string{"200", `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`},
	)
	resp, err := streamVertex(c, []message.Message{vertexUser("list")})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	call := resp.ToolCalls[0]
	assert.Equal(t, base64.StdEncoding.EncodeToString(sig), call.ThoughtSignature)

	history := []message.Message{
		vertexUser("list"),
		{Role: message.Assistant, Parts: []message.ContentPart{call}},
		makeVertexToolMsg(message.ToolResult{ToolCallID: call.ID, Name: "bash", Content: "a.txt"}),
	}
	_, err = streamVertex(c, history)
	require.NoError(t, err)
	assert.Contains(t, bodies()[1], fmt.Sprintf(`"thoughtSignature":%q`, base64.StdEncoding.EncodeToString(sig)))
}

func TestVertexGemini_RejectedSignatureIsRetriedWithStandIns(t *testing.T) {
	t.Parallel()
	c, bodies := recordingGemini(t, "gemini-3-pro-preview",
		[2]string{"400", `{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`},
		[2]string{"200", `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":500,"cachedContentTokenCount":400,"candidatesTokenCount":2,"totalTokenCount":502}}`},
	)
	call := message.ToolCall{ID: "call_1", Name: "bash", Input: `{}`, ThoughtSignature: base64.StdEncoding.EncodeToString([]byte("stale"))}
	history := []message.Message{
		vertexUser("list"),
		{Role: message.Assistant, Parts: []message.ContentPart{call}},
		makeVertexToolMsg(message.ToolResult{ToolCallID: "call_1", Name: "bash", Content: "a.txt"}),
	}
	resp, err := streamVertex(c, history)
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content)
	got := bodies()
	require.Len(t, got, 2)
	assert.Contains(t, got[1], `"thoughtSignature":"skip/thought/signature/validator"`)
	assert.Equal(t, llm.TokenUsage{TokenCount: 502, InputTokens: 100, OutputTokens: 2, CachedInputTokens: 400, CacheReadInputTokens: 400}, resp.Usage)
}

// Vertex sent every property as a bare type: integers as NUMBER, arrays with
// no items, and no enum, format, nullable or nested properties at all.
func TestVertexGemini_ToolSchemaIsFaithful(t *testing.T) {
	t.Parallel()
	tool := tools.NewSchemaOnlyTool("t", "t", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"n":    map[string]any{"type": "integer", "format": "int32"},
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"a", "b"}}},
			"opt":  map[string]any{"type": []any{"string", "null"}},
		},
	})
	params := newTestVertexAIClient().convertTools([]tools.Tool{tool}).FunctionDeclarations[0].Parameters
	assert.Equal(t, &genai.Schema{Type: genai.TypeInteger, Format: "int32"}, params.Properties["n"])
	assert.Equal(t, &genai.Schema{Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString, Enum: []string{"a", "b"}}}, params.Properties["tags"])
	assert.Equal(t, &genai.Schema{Type: genai.TypeString, Nullable: genai.Ptr(true)}, params.Properties["opt"])
}
