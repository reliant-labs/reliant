// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/responseswire"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// streamResponsesEvents serves each JSON event as one SSE frame (its "type"
// becomes the event name) through the Responses endpoint and returns every
// driver event, errors included.
func streamResponsesEvents(t *testing.T, events ...string) []llm.DriverEvent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			var head struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(e), &head); err != nil {
				t.Errorf("bad fixture event %s: %v", e, err)
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, e)
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient(llm.DriverOptions{
		Model:     models.Model{ID: models.GPT55, APIModel: "gpt-5.5", PreferredEndpoint: "responses"},
		ApiKey:    "k",
		BaseURL:   srv.URL,
		MaxTokens: 1024,
	})
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}

	var out []llm.DriverEvent
	for ev := range c.StreamResponse(context.Background(), nil, msgs, nil) {
		out = append(out, ev)
	}
	return out
}

// See the Codex driver's twin of this test: a Responses stream that ends
// cleanly before any terminal event is a response cut short, and must be a
// retryable error rather than a clean end of turn.
func TestStreamResponses_NoTerminalEventIsRetryableError(t *testing.T) {
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "text deltas only",
			events: []string{
				`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
				`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Here is the first ha"}`,
			},
		},
		{
			name: "function call arguments mid-stream",
			events: []string{
				`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"rm -"}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var streamErr error
			for _, ev := range streamResponsesEvents(t, tt.events...) {
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

// Tool calls of an incomplete or failed response must not run; a completed
// response's calls are unchanged.
func TestStreamResponses_ToolCallsOfAnUnfinishedResponseDoNotRun(t *testing.T) {
	tests := []struct {
		name       string
		events     []string
		wantReason message.FinishReason
		wantCalls  []string
	}{
		{
			name: "incomplete max_output_tokens with a half-written call",
			events: []string{
				`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"rm -"}`,
				`{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"rm -","status":"incomplete"}]}}`,
			},
			wantReason: message.FinishReasonMaxTokens,
		},
		{
			name: "incomplete keeps only the call it confirms completed",
			events: []string{
				`{"type":"response.incomplete","sequence_number":0,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}","status":"completed"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read","arguments":"{\"path\":","status":"in_progress"}]}}`,
			},
			wantReason: message.FinishReasonPauseTurn,
			wantCalls:  []string{"call_1"},
		},
		{
			name: "failed drops every call, even a completed item",
			events: []string{
				`{"type":"response.failed","sequence_number":0,"response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"upstream exploded"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`,
			},
			wantReason: message.FinishReasonError,
		},
		{
			name: "completed keeps its calls",
			events: []string{
				`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"ls\"}"}`,
				`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`,
			},
			wantReason: message.FinishReasonToolUse,
			wantCalls:  []string{"call_1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *llm.DriverResponse
			for _, ev := range streamResponsesEvents(t, tt.events...) {
				switch ev.Type {
				case llm.EventError:
					t.Fatalf("stream error: %v", ev.Error)
				case llm.EventComplete:
					got = ev.Response
				}
			}
			if got == nil {
				t.Fatal("stream produced no EventComplete")
			}
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
