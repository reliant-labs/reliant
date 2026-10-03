// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// sseResponsesServer serves a fixed list of SSE events as a Responses stream.
//
// The whole point of driving the real SDK stream rather than calling
// resolveFinishReason directly is that the two defects being fixed here live in
// the stream LOOP, not in the mapping: response.incomplete and response.failed
// used to fall through to `default:`, and `end_turn` has to survive the SDK's
// own event decoding before any mapping can see it. A test that constructed a
// responses.Response by hand would pass against both the broken and the fixed
// loop.
func sseResponsesServer(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range events {
			fmt.Fprint(w, ev)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sseEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// streamOnce runs StreamResponse against srv and returns the terminal response.
func streamOnce(t *testing.T, srv *httptest.Server) *llm.DriverResponse {
	t.Helper()
	client := &CodexClient{
		options: llm.DriverOptions{
			Model:     models.Model{ID: models.GPT53Codex, APIModel: "gpt-5.3-codex"},
			MaxTokens: 1024,
		},
		sessionID: "test-session",
		client: llm.NewOpenAISDKClient(
			option.WithBaseURL(srv.URL),
			option.WithAPIKey("test-key"),
			option.WithMaxRetries(0),
		),
	}

	messages := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}}

	var final *llm.DriverResponse
	for ev := range client.StreamResponse(context.Background(), []string{"be helpful"}, messages, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("stream error: %v", ev.Error)
		}
		if ev.Type == llm.EventComplete {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream produced no EventComplete")
	}
	return final
}

// TestStreamResponse_CompletedWithEndTurnFalseIsPauseTurn pins the defect from
// specs/stop-reason-normalization.md: a text-only turn whose response carries
// `end_turn: false` is the provider asking to continue, and reporting it as
// EndTurn is what ended the chat mid-task.
func TestStreamResponse_CompletedWithEndTurnFalseIsPauseTurn(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
		sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"I'll inspect the local dev auth setup."}`),
		sseEvent("response.completed", `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","end_turn":false,"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"I'll inspect the local dev auth setup.","annotations":[]}]}]}}`),
	)

	got := streamOnce(t, srv)

	if got.FinishReason != message.FinishReasonPauseTurn {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, message.FinishReasonPauseTurn)
	}
	if !strings.Contains(got.Content, "local dev auth setup") {
		t.Errorf("Content = %q, want the streamed text", got.Content)
	}
	if len(got.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %d, want 0", len(got.ToolCalls))
	}
}

// TestStreamResponse_CompletedWithoutEndTurnIsEndTurn is the control: the same
// stream without the field must still end the turn, so the fix above cannot be
// a blanket "never end".
func TestStreamResponse_CompletedWithoutEndTurnIsEndTurn(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":0,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Done."}`),
		sseEvent("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"Done.","annotations":[]}]}]}}`),
	)

	if got := streamOnce(t, srv); got.FinishReason != message.FinishReasonEndTurn {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, message.FinishReasonEndTurn)
	}
}

// TestStreamResponse_IncompleteMaxOutputTokensIsMaxTokens covers the ignored
// terminal event: response.incomplete used to fall through to `default:`, so
// finalResp stayed nil and a truncated turn was reported as a clean end.
func TestStreamResponse_IncompleteMaxOutputTokensIsMaxTokens(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":0,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Here is the first half"}`),
		sseEvent("response.incomplete", `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","id":"msg_1","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"Here is the first half","annotations":[]}]}]}}`),
	)

	got := streamOnce(t, srv)

	if got.FinishReason != message.FinishReasonMaxTokens {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, message.FinishReasonMaxTokens)
	}
	if !strings.Contains(got.Content, "first half") {
		t.Errorf("Content = %q, want the partial text preserved", got.Content)
	}
}

// TestStreamResponse_IncompleteInterruptedIsPauseTurn is the second signal
// Codex CLI treats as "keep going" — it normalizes this status/reason pair to
// end_turn: Some(false).
func TestStreamResponse_IncompleteInterruptedIsPauseTurn(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":0,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Working on it"}`),
		sseEvent("response.incomplete", `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"interrupted"}}}`),
	)

	if got := streamOnce(t, srv); got.FinishReason != message.FinishReasonPauseTurn {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, message.FinishReasonPauseTurn)
	}
}

// TestStreamResponse_FailedIsError covers the other previously-ignored terminal
// event.
func TestStreamResponse_FailedIsError(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.failed", `{"type":"response.failed","sequence_number":0,"response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"upstream exploded"}}}`),
	)

	if got := streamOnce(t, srv); got.FinishReason != message.FinishReasonError {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, message.FinishReasonError)
	}
}

// TestStreamResponse_ToolCallOverridesEndTurnFalse pins the precedence: a
// requested tool call makes the turn ToolUse even when the provider also asked
// to continue, because the tool result is the continuation.
func TestStreamResponse_ToolCallOverridesEndTurnFalse(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.completed", `{"type":"response.completed","sequence_number":0,"response":{"id":"resp_1","status":"completed","end_turn":false,"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`),
	)

	got := streamOnce(t, srv)

	if got.FinishReason != message.FinishReasonToolUse {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, message.FinishReasonToolUse)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Name != "bash" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", got.ToolCalls[0].Name, "bash")
	}
}
