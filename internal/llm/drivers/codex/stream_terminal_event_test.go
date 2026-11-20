// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/responseswire"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// streamEvents runs StreamResponse against srv and returns every event, errors
// included, so a test can assert that a stream ended in EventError.
func streamEvents(t *testing.T, srv *httptest.Server) []llm.DriverEvent {
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
	var events []llm.DriverEvent
	for ev := range client.StreamResponse(context.Background(), []string{"be helpful"}, messages, nil) {
		events = append(events, ev)
	}
	return events
}

// A stream that ends cleanly but before response.completed / incomplete /
// failed is a response cut short — a proxy closing the HTTP/2 stream looks
// exactly like this. It used to settle as EndTurn (or ToolUse), ending the run
// as a success with half-streamed text and silently losing any tool call whose
// arguments were still arriving. It must be an error the runtime retries, as
// codex-rs does ("stream closed before response.completed").
func TestStreamResponse_NoTerminalEventIsRetryableError(t *testing.T) {
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "text deltas only",
			events: []string{
				sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
				sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Here is the first ha"}`),
			},
		},
		{
			name: "function call arguments mid-stream",
			events: []string{
				sseEvent("response.output_item.added", `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"","status":"in_progress"}}`),
				sseEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"rm -"}`),
			},
		},
		{
			name: "complete function call item but no terminal event",
			events: []string{
				sseEvent("response.output_item.done", `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := streamEvents(t, sseResponsesServer(t, tt.events...))

			var streamErr error
			for _, ev := range events {
				switch ev.Type {
				case llm.EventComplete:
					t.Fatalf("stream with no terminal event produced EventComplete: %+v", ev.Response)
				case llm.EventError:
					streamErr = ev.Error
				}
			}
			if streamErr == nil {
				t.Fatal("stream with no terminal event produced no EventError")
			}
			if !errors.Is(streamErr, responseswire.ErrStreamEndedWithoutTerminalEvent) {
				t.Errorf("error = %v, want ErrStreamEndedWithoutTerminalEvent", streamErr)
			}
			if !errors.Is(streamErr, llm.ErrStreamEndedEarly) {
				t.Errorf("error = %v, want it to match llm.ErrStreamEndedEarly (the transient class)", streamErr)
			}
		})
	}
}

// A function call of a response that did not complete must not run: an
// incomplete, failed or cancelled response can carry a call whose arguments
// were cut off, and the agent loop executes whatever tool calls a turn returns
// before it looks at the finish reason. Only a call the incomplete response
// itself lists as a completed item survives.
func TestStreamResponse_ToolCallsOfAnUnfinishedResponseDoNotRun(t *testing.T) {
	tests := []struct {
		name       string
		events     []string
		wantReason message.FinishReason
		wantCalls  []string
	}{
		{
			name: "incomplete max_output_tokens with a half-written call",
			events: []string{
				sseEvent("response.output_item.added", `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"","status":"in_progress"}}`),
				sseEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"rm -"}`),
				sseEvent("response.incomplete", `{"type":"response.incomplete","sequence_number":2,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"rm -","status":"incomplete"}]}}`),
			},
			wantReason: message.FinishReasonMaxTokens,
		},
		{
			name: "incomplete keeps only the call it confirms completed",
			events: []string{
				sseEvent("response.output_item.done", `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}","status":"completed"}}`),
				sseEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_2","output_index":1,"delta":"{\"path\":"}`),
				sseEvent("response.incomplete", `{"type":"response.incomplete","sequence_number":2,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}","status":"completed"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read","arguments":"{\"path\":","status":"in_progress"}]}}`),
			},
			wantReason: message.FinishReasonPauseTurn,
			wantCalls:  []string{"call_1"},
		},
		{
			name: "failed drops every call, even a completed item",
			events: []string{
				sseEvent("response.output_item.done", `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}}`),
				sseEvent("response.failed", `{"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"upstream exploded"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`),
			},
			wantReason: message.FinishReasonError,
		},
		{
			name: "completed keeps its calls",
			events: []string{
				sseEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"ls\"}"}`),
				sseEvent("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`),
			},
			wantReason: message.FinishReasonToolUse,
			wantCalls:  []string{"call_1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := streamOnce(t, sseResponsesServer(t, tt.events...))

			if got.FinishReason != tt.wantReason {
				t.Errorf("FinishReason = %q, want %q", got.FinishReason, tt.wantReason)
			}
			var ids []string
			for _, tc := range got.ToolCalls {
				ids = append(ids, tc.ID)
			}
			if len(ids) != len(tt.wantCalls) {
				t.Fatalf("ToolCalls = %v, want %v", ids, tt.wantCalls)
			}
			for i := range ids {
				if ids[i] != tt.wantCalls[i] {
					t.Errorf("ToolCalls = %v, want %v", ids, tt.wantCalls)
				}
			}
		})
	}
}
