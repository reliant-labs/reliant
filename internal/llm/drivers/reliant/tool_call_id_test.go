// Copyright (c) 2025 Reliant Labs
package reliant

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

// The gateway relays the upstream provider's tool call ids, which can repeat
// across responses (a server numbering calls call_0 every turn) or be
// missing. Reliant keys a call's record, result and (for a spawn) report by
// the id, so this driver mints every call's id (llm.NewToolCallID) and the
// transcript -- and so the next request -- carries the minted one.
//
// The exception is the one part of the gateway's id the next request needs:
// for Gemini, LiteLLM appends "__thought__<signature>" to the id, and reads
// the signature back from the id it is sent. Gemini 3 rejects a function call
// in history without its signature, so that suffix is carried onto ours.

var mintedToolCallID = regexp.MustCompile(`^call_[0-9a-f]{32}$`)

type fakeGateway struct {
	mu     sync.Mutex
	bodies []string
	stream string
}

func (f *fakeGateway) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, f.stream)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func gatewayStream(toolCalls ...string) string {
	var b strings.Builder
	for i, tc := range toolCalls {
		b.WriteString(`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{` +
			tc + `,"index":` + string(rune('0'+i)) + `,"type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n")
	}
	b.WriteString(`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func streamTurn(t *testing.T, client *ReliantClient, history []message.Message) []message.ToolCall {
	t.Helper()
	var final *llm.DriverResponse
	for ev := range client.StreamResponse(context.Background(), nil, history, nil) {
		switch ev.Type {
		case llm.EventComplete:
			final = ev.Response
		case llm.EventError:
			require.NoError(t, ev.Error)
		}
	}
	require.NotNil(t, final, "stream ended without a complete event")
	return final.ToolCalls
}

func userGo() []message.Message {
	return []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}}}
}

func afterTurn(call message.ToolCall) []message.Message {
	return append(userGo(),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{call}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: call.ID, Name: call.Name, Content: "turn 1 output"}}},
	)
}

func newGatewayClient(baseURL string) *ReliantClient {
	return NewClient(llm.DriverOptions{ApiKey: "test", BaseURL: baseURL, Model: models.Model{ID: "test-model"}})
}

func TestConsecutiveResponsesReusingCallZeroGetDistinctIDs(t *testing.T) {
	f := &fakeGateway{stream: gatewayStream(`"id":"call_0"`)}
	client := newGatewayClient(f.serve(t).URL)

	first := streamTurn(t, client, userGo())
	require.Len(t, first, 1)
	second := streamTurn(t, client, afterTurn(first[0]))
	require.Len(t, second, 1)

	require.NotEqual(t, first[0].ID, second[0].ID, "both responses' call_0 must not become one call")
	require.Regexp(t, mintedToolCallID, first[0].ID)
	require.Regexp(t, mintedToolCallID, second[0].ID)
	f.mu.Lock()
	sent := f.bodies[1]
	f.mu.Unlock()
	require.Equal(t, 2, strings.Count(sent, first[0].ID), "the second request must name the minted id on the tool call and on its result: %s", sent)
	require.NotContains(t, sent, `"call_0"`, "the gateway's id must not be sent back")
}

func TestToolCallWithoutAnIDIsKeptUnderAMintedOne(t *testing.T) {
	f := &fakeGateway{stream: gatewayStream(`"id":""`)}
	calls := streamTurn(t, newGatewayClient(f.serve(t).URL), userGo())
	require.Len(t, calls, 1, "a call the gateway sent without an id must not be dropped")
	require.Regexp(t, mintedToolCallID, calls[0].ID)
}

func TestGeminiThoughtSignatureInTheGatewayIDSurvives(t *testing.T) {
	const signed = "call_9f2a7c__thought__Q2lnbmF0dXJlKytBQUE9PQ=="
	f := &fakeGateway{stream: gatewayStream(`"id":"` + signed + `"`)}
	client := newGatewayClient(f.serve(t).URL)

	first := streamTurn(t, client, userGo())
	require.Len(t, first, 1)
	base, signature, ok := strings.Cut(first[0].ID, "__thought__")
	require.True(t, ok, "the thought signature must be carried onto the minted id: %q", first[0].ID)
	require.Regexp(t, mintedToolCallID, base, "the part before the signature is ours, not the gateway's")
	require.Equal(t, "Q2lnbmF0dXJlKytBQUE9PQ==", signature)

	again := streamTurn(t, client, userGo())
	require.NotEqual(t, first[0].ID, again[0].ID, "the same gateway id twice is still two calls")

	streamTurn(t, client, afterTurn(first[0]))
	f.mu.Lock()
	sent := f.bodies[2]
	f.mu.Unlock()
	require.Equal(t, 2, strings.Count(sent, first[0].ID),
		"the next request must send the signed id on the call (where LiteLLM reads the signature) and on its result: %s", sent)
}
