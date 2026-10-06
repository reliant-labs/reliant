// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A tool call's id is a GLOBAL key in reliant's persistence, not a per-turn
// label: tool_calls and tool_call_results are keyed by it alone, and a spawn's
// terminal report is unique per tool_call_id across every chat
// (idx_agent_messages_one_terminal_report_per_spawn). Vertex Gemini returns no
// id of its own, and this driver used the FUNCTION NAME in its place, so every
// `spawn` call in every chat was `spawn`. The second one's report is then
// "already reported" and silently dropped, and a second call to any tool whose
// first call is terminal is answered from the first call's recorded result
// without running (ExecuteToolsActivity.checkPriorTerminalResult) — in another
// chat as readily as in this one.

// geminiSSE serves each request the next canned response as a one-event SSE
// stream, the wire shape streamGenerateContent?alt=sse uses.
func geminiSSE(t *testing.T, bodies ...string) *httptest.Server {
	t.Helper()
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.Less(t, next, len(bodies), "unexpected extra request")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", bodies[next])
		next++
	}))
	t.Cleanup(srv.Close)
	return srv
}

// functionCallsResponse is a candidate whose parts are function calls with the
// given names and no ids, as Vertex Gemini returns them.
func functionCallsResponse(names ...string) string {
	parts := ""
	for i, name := range names {
		if i > 0 {
			parts += ","
		}
		parts += fmt.Sprintf(`{"functionCall":{"name":%q,"args":{"preset":"general"}}}`, name)
	}
	return fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[%s]},"finishReason":"STOP"}]}`, parts)
}

func newStreamingGeminiClient(t *testing.T, srv *httptest.Server) *VertexAIClient {
	t.Helper()
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      "test-key",
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	require.NoError(t, err)
	return &VertexAIClient{
		options:      llm.DriverOptions{Model: models.Model{APIModel: "gemini-2.5-pro"}},
		provider:     providerGemini,
		geminiClient: client,
	}
}

// streamToolCalls runs one StreamResponse turn — the path CallLLM takes — and
// returns the tool calls it finished with, after checking each one's id is the
// id its tool_use_start announced (the UI's card and the persisted call are
// matched on it).
func streamToolCalls(t *testing.T, c *VertexAIClient) []message.ToolCall {
	t.Helper()
	user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "delegate"}}}
	var started []string
	var final *llm.DriverResponse
	for ev := range c.StreamResponse(context.Background(), nil, []message.Message{user}, nil) {
		switch ev.Type {
		case llm.EventToolUseStart:
			started = append(started, ev.ToolCall.ID)
		case llm.EventComplete:
			final = ev.Response
		case llm.EventError:
			require.NoError(t, ev.Error)
		}
	}
	require.NotNil(t, final, "stream ended without a complete event")
	var finished []string
	for _, tc := range final.ToolCalls {
		finished = append(finished, tc.ID)
	}
	require.Equal(t, started, finished, "a tool call must keep the id its tool_use_start announced")
	return final.ToolCalls
}

func requireDistinctToolCallIDs(t *testing.T, calls []message.ToolCall) {
	t.Helper()
	seen := map[string]bool{}
	for _, tc := range calls {
		require.NotEmpty(t, tc.ID)
		require.NotEqual(t, tc.Name, tc.ID, "a tool call id must not be the tool's name: every call to %q would share it", tc.Name)
		require.False(t, seen[tc.ID], "tool call id %q was issued twice", tc.ID)
		seen[tc.ID] = true
	}
}

// Two turns that each call spawn — two chats, or one chat spawning twice —
// must not produce the same tool call id. Nor may two spawn calls in one
// response, which the runtime dispatches as two children keyed by that id.
func TestVertexGemini_StreamedToolCallIDsAreUniquePerCall(t *testing.T) {
	srv := geminiSSE(t,
		functionCallsResponse("spawn"),
		functionCallsResponse("spawn"),
		functionCallsResponse("spawn", "spawn"),
	)
	c := newStreamingGeminiClient(t, srv)

	var all []message.ToolCall
	for turn := 0; turn < 3; turn++ {
		calls := streamToolCalls(t, c)
		for _, tc := range calls {
			require.Equal(t, "spawn", tc.Name)
		}
		all = append(all, calls...)
	}
	require.Len(t, all, 4)
	requireDistinctToolCallIDs(t, all)
}

// The non-streaming path converts responses the same way.
func TestVertexGemini_ConvertedToolCallIDsAreUniquePerCall(t *testing.T) {
	c := newTestVertexAIClient()
	response := func() *genai.GenerateContentResponse {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
			Content: &genai.Content{Role: string(genai.RoleModel), Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "spawn", Args: map[string]any{"preset": "general"}}},
				{FunctionCall: &genai.FunctionCall{Name: "spawn", Args: map[string]any{"preset": "researcher"}}},
			}},
			FinishReason: genai.FinishReasonStop,
		}}}
	}

	var all []message.ToolCall
	for turn := 0; turn < 2; turn++ {
		out, err := c.convertGeminiResponse(response())
		require.NoError(t, err)
		require.Len(t, out.ToolCalls, 2)
		all = append(all, out.ToolCalls...)
	}
	requireDistinctToolCallIDs(t, all)
}

// The id is reliant's own; Vertex pairs a function response with its call by
// NAME, which convertMessagesToGemini sends regardless of the id. A minted id
// must therefore round-trip without changing what reaches the wire.
func TestVertexGemini_MintedIDDoesNotReachTheWire(t *testing.T) {
	c := newTestVertexAIClient()
	out, err := c.convertGeminiResponse(&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		Content: &genai.Content{Role: string(genai.RoleModel), Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "ls"}}},
		}},
	}}})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls, 1)
	call := out.ToolCalls[0]

	assistant := message.Message{Role: message.Assistant, Parts: []message.ContentPart{call}}
	tool := makeVertexToolMsg(message.ToolResult{ToolCallID: call.ID, Name: "bash", Content: "file.txt"})
	contents := c.convertMessagesToGemini([]message.Message{assistant, tool})

	require.Len(t, contents, 2)
	require.Equal(t, "bash", contents[0].Parts[0].FunctionCall.Name)
	require.Equal(t, "bash", contents[1].Parts[0].FunctionResponse.Name)
}
