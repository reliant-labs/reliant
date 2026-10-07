// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// OpenRouter relays whatever tool call id the upstream host chose, and the
// hosts behind it are not all careful: an id can be missing, repeated within
// one response, or repeated across responses. Reliant keys a tool call's
// record, result and (for a spawn) report by that id, so this driver mints
// every call's id itself (llm.NewToolCallID). The minted id is what the
// transcript stores and what the next request sends back on the call and on
// its result, which is all OpenRouter needs to pair them. The upstream's id
// is used only inside one response: Gemini's reasoning_details name it.

var mintedToolCallID = regexp.MustCompile(`^call_[0-9a-f]{32}$`)

func chatChunk(toolCalls string) string {
	return `data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[` + toolCalls + `]},"finish_reason":null}]}` + "\n\n"
}

const chatFinish = `data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"

func chatCompletion(toolCalls string) string {
	return `{"id":"g1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[` +
		toolCalls + `]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
}

func functionCall(id, name string) string {
	idField := ""
	if id != "" {
		idField = `"id":"` + id + `",`
	}
	return `{` + idField + `"type":"function","function":{"name":"` + name + `","arguments":"{}"}}`
}

// One response, three calls: the first carries no id, the next two share one.
var (
	unreliableIDStream = chatChunk(`{"index":0,"type":"function","function":{"name":"read_a","arguments":"{}"}}`) +
		chatChunk(`{"id":"call_0","index":1,"type":"function","function":{"name":"read_b","arguments":"{}"}}`) +
		chatChunk(`{"id":"call_0","index":2,"type":"function","function":{"name":"read_c","arguments":"{}"}}`) +
		chatFinish
	unreliableIDCompletion = chatCompletion(functionCall("", "read_a") + "," + functionCall("call_0", "read_b") + "," + functionCall("call_0", "read_c"))
)

// The Responses API shape of the same three calls.
const unreliableIDResponse = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[` +
	`{"type":"function_call","id":"fc_a","call_id":"call_0","name":"read_a","arguments":"{}","status":"completed"},` +
	`{"type":"function_call","id":"fc_b","call_id":"call_0","name":"read_b","arguments":"{}","status":"completed"},` +
	`{"type":"function_call","id":"fc_c","call_id":"call_1","name":"read_c","arguments":"{}","status":"completed"}],` +
	`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

func responsesStream(response string) string {
	return "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":" + response + "}\n\n"
}

// fakeOpenRouter answers each request in the shape its endpoint and stream
// flag ask for, and keeps every request body.
type fakeOpenRouter struct {
	mu                      sync.Mutex
	bodies, paths           []string
	stream, completion, rsp string
}

func (f *fakeOpenRouter) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(body))
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		streaming := strings.Contains(string(body), `"stream":true`)
		switch {
		case strings.HasSuffix(r.URL.Path, "/responses") && streaming:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, responsesStream(f.rsp))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, f.rsp)
		case streaming:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, f.stream)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, f.completion)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// requireEndpoint checks every request went to the endpoint the path is for,
// so a subtest cannot pass by having been routed somewhere else.
func (f *fakeOpenRouter) requireEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.paths)
	for _, p := range f.paths {
		require.Equal(t, endpoint == "responses", strings.HasSuffix(p, "/responses"), "request went to %s", p)
	}
}

func (f *fakeOpenRouter) body(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

// Every request path that builds tool calls.
var toolCallIDPaths = []struct {
	name     string
	apiModel string
	endpoint string
}{
	{"anthropic (cache-control path)", "anthropic/claude-sonnet-4.5", ""},
	{"gemini (reasoning-details path)", "google/gemini-2.5-pro", ""},
	{"other (OpenAI-compatible chat completions)", "meta-llama/llama-3.3-70b-instruct", ""},
	{"other (OpenAI-compatible responses API)", "openai/gpt-5.4", "responses"},
}

func newToolCallIDTestClient(baseURL, apiModel, endpoint string) *Client {
	return NewClient(llm.DriverOptions{
		ApiKey:    "test-key",
		BaseURL:   baseURL,
		Model:     models.Model{ID: models.ModelID("tool-call-id-test"), APIModel: apiModel, PreferredEndpoint: endpoint},
		MaxTokens: 1024,
	})
}

func userGo() []message.Message {
	return []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}}}
}

// streamTurn runs one StreamResponse turn and checks a tool_use_start, where
// the path emits one, announced the id the call finished with.
func streamTurn(t *testing.T, client *Client, history []message.Message, checkStarts bool) []message.ToolCall {
	t.Helper()
	var started []string
	var final *llm.DriverResponse
	for ev := range client.StreamResponse(context.Background(), nil, history, nil) {
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
	if checkStarts && len(started) > 0 {
		var finished []string
		for _, tc := range final.ToolCalls {
			finished = append(finished, tc.ID)
		}
		require.Equal(t, started, finished, "a tool call must keep the id its tool_use_start announced")
	}
	return final.ToolCalls
}

func requireMintedToolCallIDs(t *testing.T, calls []message.ToolCall) {
	t.Helper()
	var names []string
	for _, tc := range calls {
		names = append(names, tc.Name)
	}
	require.ElementsMatch(t, []string{"read_a", "read_b", "read_c"}, names, "a call without an id must not be dropped")
	seen := map[string]bool{}
	for _, tc := range calls {
		require.Regexp(t, mintedToolCallID, tc.ID, "%s's id must be minted, not the upstream's", tc.Name)
		require.False(t, seen[tc.ID], "id %q is used by two calls in one response", tc.ID)
		seen[tc.ID] = true
	}
}

func TestStreamResponseGivesEveryToolCallItsOwnID(t *testing.T) {
	for _, path := range toolCallIDPaths {
		t.Run(path.name, func(t *testing.T) {
			f := &fakeOpenRouter{stream: unreliableIDStream, completion: unreliableIDCompletion, rsp: unreliableIDResponse}
			client := newToolCallIDTestClient(f.serve(t).URL, path.apiModel, path.endpoint)
			// The Responses stream announces a call by its item id before
			// call_id is known; that pre-dates minting and is not checked.
			requireMintedToolCallIDs(t, streamTurn(t, client, userGo(), path.endpoint == ""))
			f.requireEndpoint(t, path.endpoint)
		})
	}
}

func TestSendMessagesGivesEveryToolCallItsOwnID(t *testing.T) {
	for _, path := range toolCallIDPaths {
		t.Run(path.name, func(t *testing.T) {
			f := &fakeOpenRouter{stream: unreliableIDStream, completion: unreliableIDCompletion, rsp: unreliableIDResponse}
			client := newToolCallIDTestClient(f.serve(t).URL, path.apiModel, path.endpoint)
			resp, err := client.SendMessages(context.Background(), nil, userGo(), nil)
			require.NoError(t, err)
			requireMintedToolCallIDs(t, resp.ToolCalls)
			f.requireEndpoint(t, path.endpoint)
		})
	}
}

// Two consecutive responses that both say call_0 are two calls, on every
// path, and the request after the first carries the minted id on the call and
// on its result -- never the upstream's call_0.
func TestConsecutiveResponsesReusingCallZeroGetDistinctIDs(t *testing.T) {
	callZero := chatChunk(`{"id":"call_0","index":0,"type":"function","function":{"name":"bash","arguments":"{}"}}`) + chatFinish
	callZeroResponse := `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[` +
		`{"type":"function_call","id":"fc_0","call_id":"call_0","name":"bash","arguments":"{}","status":"completed"}],` +
		`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	for _, path := range toolCallIDPaths {
		t.Run(path.name, func(t *testing.T) {
			f := &fakeOpenRouter{stream: callZero, rsp: callZeroResponse}
			client := newToolCallIDTestClient(f.serve(t).URL, path.apiModel, path.endpoint)

			first := streamTurn(t, client, userGo(), false)
			require.Len(t, first, 1)
			history := append(userGo(),
				message.Message{Role: message.Assistant, Parts: []message.ContentPart{first[0]}},
				message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: first[0].ID, Name: "bash", Content: "turn 1 output"}}},
			)
			second := streamTurn(t, client, history, false)
			require.Len(t, second, 1)

			require.NotEqual(t, first[0].ID, second[0].ID, "both responses' call_0 must not become one call")
			require.Regexp(t, mintedToolCallID, first[0].ID)
			require.Regexp(t, mintedToolCallID, second[0].ID)
			sent := f.body(1)
			require.GreaterOrEqual(t, strings.Count(sent, first[0].ID), 2,
				"the second request must name the minted id on the tool call and on its result: %s", sent)
			require.NotContains(t, sent, `"call_0"`, "the upstream's id must not be sent back")
			f.requireEndpoint(t, path.endpoint)
		})
	}
}

// Gemini's thought signatures arrive in reasoning_details keyed by the
// upstream's tool call id -- the one place this driver still needs that id.
// The signature must land on the call under its minted id, and go back out
// keyed by the minted id, which is what the next request's tool call carries.
func TestGeminiThoughtSignatureFollowsTheMintedID(t *testing.T) {
	stream := `data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"",` +
		`"reasoning_details":[{"type":"reasoning.encrypted","id":"call_0","data":"SIG-STREAM"}],` +
		`"tool_calls":[{"id":"call_0","index":0,"type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n" + chatFinish
	completion := `{"id":"g1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"",` +
		`"reasoning_details":[{"type":"reasoning.encrypted","id":"call_0","data":"SIG-SEND"}],` +
		`"tool_calls":[` + functionCall("call_0", "bash") + `]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	f := &fakeOpenRouter{stream: stream, completion: completion}
	client := newToolCallIDTestClient(f.serve(t).URL, "google/gemini-3-pro-preview", "")

	streamed := streamTurn(t, client, userGo(), true)
	require.Len(t, streamed, 1)
	require.Regexp(t, mintedToolCallID, streamed[0].ID)
	require.Equal(t, "SIG-STREAM", streamed[0].ThoughtSignature)

	sent, err := client.SendMessages(context.Background(), nil, userGo(), nil)
	require.NoError(t, err)
	require.Len(t, sent.ToolCalls, 1)
	require.Regexp(t, mintedToolCallID, sent.ToolCalls[0].ID)
	require.Equal(t, "SIG-SEND", sent.ToolCalls[0].ThoughtSignature)

	history := append(userGo(),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{streamed[0]}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: streamed[0].ID, Name: "bash", Content: "ok"}}},
	)
	_, err = client.SendMessages(context.Background(), nil, history, nil)
	require.NoError(t, err)
	next := f.body(2)
	require.Contains(t, next, `"data":"SIG-STREAM"`)
	require.Equal(t, 3, strings.Count(next, streamed[0].ID),
		"the minted id must key the tool call, its reasoning detail and its result: %s", next)
}
