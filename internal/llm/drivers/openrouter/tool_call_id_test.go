// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// OpenRouter relays whatever tool call id the upstream host chose, and the
// hosts behind it are not all careful: an id can be missing, or repeated
// within one response. Reliant keys a tool call's record, result and (for a
// spawn) report by that id, so every call must leave this driver with an id
// that is non-empty and not shared with another call in the same response. A
// provider id that is already both is kept as sent.

// One response, three calls: the first carries no id, the next two share one.
const unreliableIDStream = `data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"type":"function","function":{"name":"read_a","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_0","index":1,"type":"function","function":{"name":"read_b","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_0","index":2,"type":"function","function":{"name":"read_c","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}

data: [DONE]

`

const unreliableIDCompletion = `{"id":"g1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[` +
	`{"id":"","type":"function","function":{"name":"read_a","arguments":"{}"}},` +
	`{"id":"call_0","type":"function","function":{"name":"read_b","arguments":"{}"}},` +
	`{"id":"call_0","type":"function","function":{"name":"read_c","arguments":"{}"}}]}}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// unreliableIDServer answers a streaming request with unreliableIDStream and
// any other with unreliableIDCompletion.
func unreliableIDServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, unreliableIDStream)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, unreliableIDCompletion)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Each of the driver's three request paths builds tool calls itself.
var toolCallIDPaths = []struct {
	name     string
	apiModel string
}{
	{"anthropic (cache-control path)", "anthropic/claude-sonnet-4.5"},
	{"gemini (reasoning-details path)", "google/gemini-2.5-pro"},
	{"other (OpenAI-compatible path)", "meta-llama/llama-3.3-70b-instruct"},
}

func newToolCallIDTestClient(baseURL, apiModel string) *Client {
	return NewClient(llm.DriverOptions{
		ApiKey:    "test-key",
		BaseURL:   baseURL,
		Model:     models.Model{ID: models.ModelID("tool-call-id-test"), APIModel: apiModel},
		MaxTokens: 1024,
	})
}

func requireUsableToolCallIDs(t *testing.T, calls []message.ToolCall) {
	t.Helper()
	var names []string
	for _, tc := range calls {
		names = append(names, tc.Name)
	}
	require.Equal(t, []string{"read_a", "read_b", "read_c"}, names, "a call without an id must not be dropped")
	seen := map[string]bool{}
	for _, tc := range calls {
		require.NotEmpty(t, tc.ID, "%s has no id", tc.Name)
		require.False(t, seen[tc.ID], "id %q is used by two calls in one response", tc.ID)
		seen[tc.ID] = true
	}
	require.Equal(t, "call_0", calls[1].ID, "a non-empty id not yet used in the response must be kept as sent")
	require.True(t, strings.HasPrefix(calls[0].ID, "call_"), "synthesized id %q", calls[0].ID)
	require.True(t, strings.HasPrefix(calls[2].ID, "call_"), "synthesized id %q", calls[2].ID)
}

func TestStreamResponseGivesEveryToolCallItsOwnID(t *testing.T) {
	for _, path := range toolCallIDPaths {
		t.Run(path.name, func(t *testing.T) {
			srv := unreliableIDServer(t)
			client := newToolCallIDTestClient(srv.URL, path.apiModel)
			user := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}}}

			var started []string
			var final *llm.DriverResponse
			for ev := range client.StreamResponse(context.Background(), nil, user, nil) {
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
			requireUsableToolCallIDs(t, final.ToolCalls)
			if len(started) > 0 {
				var finished []string
				for _, tc := range final.ToolCalls {
					finished = append(finished, tc.ID)
				}
				require.Equal(t, started, finished, "a tool call must keep the id its tool_use_start announced")
			}
		})
	}
}

func TestSendMessagesGivesEveryToolCallItsOwnID(t *testing.T) {
	for _, path := range toolCallIDPaths {
		t.Run(path.name, func(t *testing.T) {
			srv := unreliableIDServer(t)
			client := newToolCallIDTestClient(srv.URL, path.apiModel)
			user := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}}}

			resp, err := client.SendMessages(context.Background(), nil, user, nil)
			require.NoError(t, err)
			requireUsableToolCallIDs(t, resp.ToolCalls)
		})
	}
}
