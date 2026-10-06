// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// A local OpenAI-compatible server chooses its own tool call ids, and nothing
// makes them good ones: some omit the id, some number calls from call_0 in
// every response. Reliant keys a tool call's record, result and (for a spawn)
// report by that id, so a call must leave the driver with an id that is
// non-empty and not shared with another call in the same response. A provider
// id that already is both is kept: the server pairs results with calls by it.

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

// requireUsableToolCallIDs checks the three calls survived, in order, each with
// an id of its own, and that the one valid provider id was kept.
func requireUsableToolCallIDs(t *testing.T, calls []message.ToolCall) {
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
		if tc.ID == "" {
			t.Fatalf("%s has no id", tc.Name)
		}
		if seen[tc.ID] {
			t.Fatalf("id %q is used by two calls in one response: %+v", tc.ID, calls)
		}
		seen[tc.ID] = true
	}
	if calls[1].ID != "call_0" {
		t.Errorf("read_b's id = %q; a non-empty id not yet used in the response must be kept as the provider sent it", calls[1].ID)
	}
	for _, i := range []int{0, 2} {
		if !strings.HasPrefix(calls[i].ID, "call_") {
			t.Errorf("%s's id = %q; a synthesized id must look like call_<uuid>", calls[i].Name, calls[i].ID)
		}
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
	requireUsableToolCallIDs(t, got.complete.ToolCalls)
}

func TestSendMessagesGivesEveryToolCallItsOwnID(t *testing.T) {
	srv := jsonServer(t, unreliableIDCompletion)
	resp, err := newTestClient(srv.URL, nil).SendMessages(context.Background(), nil, userTurn("go"), nil)
	if err != nil {
		t.Fatalf("SendMessages: %v", err)
	}
	requireUsableToolCallIDs(t, resp.ToolCalls)
}
