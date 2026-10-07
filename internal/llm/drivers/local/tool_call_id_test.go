// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// A local OpenAI-compatible server chooses its own tool call ids, and nothing
// makes them good ones: some omit the id, some number calls from call_0 in
// every response. Reliant keys a tool call's record, result and (for a spawn)
// report by that id, so the driver mints every call's id itself
// (llm.NewToolCallID). The minted id is what the transcript stores and what
// the next request sends back, on the call and on its result alike -- all an
// OpenAI-compatible server needs to pair them.

// mintedToolCallID is llm.NewToolCallID's shape.
var mintedToolCallID = regexp.MustCompile(`^call_[0-9a-f]{32}$`)

// One response, three calls: the first carries no id, the next two share one.
const unreliableIDStream = `data: {"id":"c9","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"type":"function","function":{"name":"read_a","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"c9","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_0","index":1,"type":"function","function":{"name":"read_b","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"c9","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_0","index":2,"type":"function","function":{"name":"read_c","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"c9","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}

data: [DONE]

`

const unreliableIDCompletion = `{"id":"c9","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[` +
	`{"id":"","type":"function","function":{"name":"read_a","arguments":"{}"}},` +
	`{"id":"call_0","type":"function","function":{"name":"read_b","arguments":"{}"}},` +
	`{"id":"call_0","type":"function","function":{"name":"read_c","arguments":"{}"}}]}}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// What a server that numbers calls per response sends on EVERY turn.
const callZeroStream = `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_0","index":0,"type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// requireMintedToolCallIDs checks the three calls survived, in order, each
// with a minted id of its own -- none of them the server's.
func requireMintedToolCallIDs(t *testing.T, calls []message.ToolCall) {
	t.Helper()
	var names []string
	for _, tc := range calls {
		names = append(names, tc.Name)
	}
	if strings.Join(names, ",") != "read_a,read_b,read_c" {
		t.Fatalf("tool calls = %v, want read_a,read_b,read_c: a call without an id must not be dropped", names)
	}
	seen := map[string]bool{}
	for _, tc := range calls {
		if !mintedToolCallID.MatchString(tc.ID) {
			t.Fatalf("%s's id = %q, want a minted call_<32 hex>", tc.Name, tc.ID)
		}
		if seen[tc.ID] {
			t.Fatalf("id %q is used by two calls in one response: %+v", tc.ID, calls)
		}
		seen[tc.ID] = true
	}
}

func TestStreamGivesEveryToolCallItsOwnID(t *testing.T) {
	srv := sseServer(t, unreliableIDStream, nil)
	got := collect(newTestClient(srv.URL, nil).StreamResponse(context.Background(), nil, userTurn("go"), nil))
	if got.err != nil {
		t.Fatalf("stream error: %v", got.err)
	}
	if got.complete == nil {
		t.Fatal("no EventComplete")
	}
	requireMintedToolCallIDs(t, got.complete.ToolCalls)
}

func TestSendMessagesGivesEveryToolCallItsOwnID(t *testing.T) {
	srv := jsonServer(t, unreliableIDCompletion)
	resp, err := newTestClient(srv.URL, nil).SendMessages(context.Background(), nil, userTurn("go"), nil)
	if err != nil {
		t.Fatalf("SendMessages: %v", err)
	}
	requireMintedToolCallIDs(t, resp.ToolCalls)
}

// Two consecutive responses that both say call_0 are two calls. Keeping the
// server's id -- even one unique within its response -- made turn 2's call
// the same call as turn 1's, which execute_tools then answered from turn 1's
// record without running it. The next request must carry the minted id on the
// call AND on its result, since that is how the server pairs them.
func TestConsecutiveResponsesReusingCallZeroGetDistinctIDs(t *testing.T) {
	var secondRequest string
	requests := 0
	srv := sseServer(t, callZeroStream, func(_ *http.Request, body []byte) {
		requests++
		if requests == 2 {
			secondRequest = string(body)
		}
	})
	client := newTestClient(srv.URL, nil)

	first := collect(client.StreamResponse(context.Background(), nil, userTurn("go"), nil))
	if first.err != nil || first.complete == nil || len(first.complete.ToolCalls) != 1 {
		t.Fatalf("first response = %+v (err %v)", first.complete, first.err)
	}
	call1 := first.complete.ToolCalls[0]

	history := append(userTurn("go"),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{call1}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: call1.ID, Name: "bash", Content: "turn 1 output"}}},
	)
	second := collect(client.StreamResponse(context.Background(), nil, history, nil))
	if second.err != nil || second.complete == nil || len(second.complete.ToolCalls) != 1 {
		t.Fatalf("second response = %+v (err %v)", second.complete, second.err)
	}
	call2 := second.complete.ToolCalls[0]

	if call1.ID == call2.ID {
		t.Fatalf("both responses' call_0 became %q: turn 2's call would be answered from turn 1's record", call1.ID)
	}
	for _, id := range []string{call1.ID, call2.ID} {
		if !mintedToolCallID.MatchString(id) {
			t.Fatalf("id %q is not minted", id)
		}
	}
	if strings.Count(secondRequest, call1.ID) != 2 {
		t.Fatalf("the request after turn 1 must name %q on the tool call and on its result; body: %s", call1.ID, secondRequest)
	}
	if strings.Contains(secondRequest, `"call_0"`) {
		t.Fatalf("the server's own id must not be sent back: %s", secondRequest)
	}
}
