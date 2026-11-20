// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
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
			if !errors.Is(streamErr, drivererrors.ErrStreamEndedEarly) {
				t.Errorf("error = %v, want it to match drivererrors.ErrStreamEndedEarly (the transient class)", streamErr)
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
		// wantDropped are the calls reported in DroppedToolCalls, by the id
		// their EventToolUseStart carried (the item id).
		wantDropped []string
		// keptStarts are started calls that ARE returned. Every other start
		// must be in DroppedToolCalls, or its card is never closed.
		keptStarts []string
	}{
		{
			name: "incomplete max_output_tokens with a half-written call",
			events: []string{
				`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"rm -"}`,
				`{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"rm -","status":"incomplete"}]}}`,
			},
			wantReason:  message.FinishReasonMaxTokens,
			wantDropped: []string{"fc_1"},
		},
		{
			name: "incomplete keeps only the call it confirms completed",
			events: []string{
				`{"type":"response.incomplete","sequence_number":0,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}","status":"completed"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read","arguments":"{\"path\":","status":"in_progress"}]}}`,
			},
			wantReason:  message.FinishReasonPauseTurn,
			wantCalls:   []string{"call_1"},
			wantDropped: []string{"fc_2"},
		},
		{
			name: "failed drops every call, even a completed item",
			events: []string{
				`{"type":"response.failed","sequence_number":0,"response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"upstream exploded"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`,
			},
			wantReason:  message.FinishReasonError,
			wantDropped: []string{"fc_1"},
		},
		{
			name: "completed keeps its calls",
			events: []string{
				`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"fc_1","output_index":0,"delta":"{\"command\":\"ls\"}"}`,
				`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}]}}`,
			},
			wantReason: message.FinishReasonToolUse,
			wantCalls:  []string{"call_1"},
			keptStarts: []string{"fc_1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := streamResponsesEvents(t, tt.events...)
			got := completeOf(t, events)
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
			assertDroppedCallsReported(t, events, got, tt.wantDropped, tt.keptStarts)
		})
	}
}

// completeOf returns the response of the stream's EventComplete, failing the
// test on an EventError or a missing EventComplete.
func completeOf(t *testing.T, events []llm.DriverEvent) *llm.DriverResponse {
	t.Helper()
	var got *llm.DriverResponse
	for _, ev := range events {
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
	return got
}

// assertDroppedCallsReported checks DroppedToolCalls against wantDropped, and
// that every call the stream opened with EventToolUseStart is either returned
// (keptStarts) or reported dropped under the id the start carried — the id
// call_llm closes the card by. A start that is neither leaves a card open.
func assertDroppedCallsReported(t *testing.T, events []llm.DriverEvent, got *llm.DriverResponse, wantDropped, keptStarts []string) {
	t.Helper()
	var dropped []string
	droppedSet := map[string]bool{}
	for _, tc := range got.DroppedToolCalls {
		dropped = append(dropped, tc.ID)
		droppedSet[tc.ID] = true
	}
	sort.Strings(dropped)
	want := append([]string(nil), wantDropped...)
	sort.Strings(want)
	if strings.Join(dropped, ",") != strings.Join(want, ",") {
		t.Errorf("DroppedToolCalls = %v, want %v", dropped, want)
	}
	kept := map[string]bool{}
	for _, id := range keptStarts {
		kept[id] = true
	}
	for _, ev := range events {
		if ev.Type != llm.EventToolUseStart || ev.ToolCall == nil {
			continue
		}
		if id := ev.ToolCall.ID; !kept[id] && !droppedSet[id] {
			t.Errorf("call opened as %q is neither returned nor reported dropped; its card would never close", id)
		}
	}
}
