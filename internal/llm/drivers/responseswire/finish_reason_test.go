// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// decode builds a responses.Response the way the SDK does, from raw JSON.
//
// Constructing the struct literally would leave JSON.ExtraFields empty, so
// every end_turn assertion would pass against a map nothing ever filled — the
// test would prove only that reading an empty map returns absent. Decoding
// real payloads is what pins the actual claim this package rests on: that an
// undocumented top-level field survives openai-go's decoder and is reachable.
func decode(t *testing.T, raw string) *responses.Response {
	t.Helper()
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return &resp
}

func TestEndTurn(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantValue   bool
		wantPresent bool
	}{
		{
			name:        "present and false is the continuation request",
			raw:         `{"id":"resp_1","status":"completed","end_turn":false}`,
			wantValue:   false,
			wantPresent: true,
		},
		{
			name:        "present and true",
			raw:         `{"id":"resp_1","status":"completed","end_turn":true}`,
			wantValue:   true,
			wantPresent: true,
		},
		{
			name:        "absent on legacy models",
			raw:         `{"id":"resp_1","status":"completed"}`,
			wantValue:   false,
			wantPresent: false,
		},
		{
			name:        "explicit null is not a boolean",
			raw:         `{"id":"resp_1","status":"completed","end_turn":null}`,
			wantValue:   false,
			wantPresent: false,
		},
		{
			name:        "non-bool value is reported absent rather than coerced",
			raw:         `{"id":"resp_1","status":"completed","end_turn":"false"}`,
			wantValue:   false,
			wantPresent: false,
		},
		{
			name:        "numeric value is reported absent rather than coerced",
			raw:         `{"id":"resp_1","status":"completed","end_turn":0}`,
			wantValue:   false,
			wantPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, present := EndTurn(decode(t, tt.raw))
			if value != tt.wantValue || present != tt.wantPresent {
				t.Errorf("EndTurn() = (%v, %v), want (%v, %v)", value, present, tt.wantValue, tt.wantPresent)
			}
		})
	}

	t.Run("nil response", func(t *testing.T) {
		if value, present := EndTurn(nil); value || present {
			t.Errorf("EndTurn(nil) = (%v, %v), want (false, false)", value, present)
		}
	})
}

func TestFinishReason(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		toolCallCount int
		want          message.FinishReason
	}{
		{
			name: "completed with no end_turn is a plain end",
			raw:  `{"id":"r","status":"completed"}`,
			want: message.FinishReasonEndTurn,
		},
		{
			name: "completed with end_turn true is a plain end",
			raw:  `{"id":"r","status":"completed","end_turn":true}`,
			want: message.FinishReasonEndTurn,
		},
		{
			name: "completed with end_turn false asks to continue",
			raw:  `{"id":"r","status":"completed","end_turn":false}`,
			want: message.FinishReasonPauseTurn,
		},
		{
			name:          "tool calls win over a completed status",
			raw:           `{"id":"r","status":"completed"}`,
			toolCallCount: 1,
			want:          message.FinishReasonToolUse,
		},
		{
			name:          "tool calls win over end_turn false",
			raw:           `{"id":"r","status":"completed","end_turn":false}`,
			toolCallCount: 2,
			want:          message.FinishReasonToolUse,
		},
		{
			name: "incomplete max_output_tokens is truncation",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`,
			want: message.FinishReasonMaxTokens,
		},
		{
			name: "incomplete content_filter is a refusal",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"}}`,
			want: message.FinishReasonRefusal,
		},
		{
			name: "incomplete interrupted asks to continue",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{"reason":"interrupted"}}`,
			want: message.FinishReasonPauseTurn,
		},
		{
			name: "incomplete max_messages is not a vocabulary we map",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_messages"}}`,
			want: message.FinishReasonUnknown,
		},
		{
			name: "incomplete with an empty reason",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{}}`,
			want: message.FinishReasonUnknown,
		},
		{
			name: "incomplete with no details at all",
			raw:  `{"id":"r","status":"incomplete"}`,
			want: message.FinishReasonUnknown,
		},
		{
			name:          "partial tool calls do not promote an incomplete response",
			raw:           `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`,
			toolCallCount: 1,
			want:          message.FinishReasonMaxTokens,
		},
		{
			name: "failed",
			raw:  `{"id":"r","status":"failed","error":{"code":"server_error","message":"boom"}}`,
			want: message.FinishReasonError,
		},
		{
			name:          "tool calls do not promote a failed response",
			raw:           `{"id":"r","status":"failed"}`,
			toolCallCount: 3,
			want:          message.FinishReasonError,
		},
		{
			name: "cancelled",
			raw:  `{"id":"r","status":"cancelled"}`,
			want: message.FinishReasonCancelled,
		},
		{
			name:          "tool calls do not promote a cancelled response",
			raw:           `{"id":"r","status":"cancelled"}`,
			toolCallCount: 1,
			want:          message.FinishReasonCancelled,
		},
		{
			name: "end_turn false on an incomplete response does not override its reason",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"end_turn":false}`,
			want: message.FinishReasonMaxTokens,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FinishReason(decode(t, tt.raw), tt.toolCallCount); got != tt.want {
				t.Errorf("FinishReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFinishReason_NilResponseKeepsTheDriverFallback(t *testing.T) {
	if got := FinishReason(nil, 0); got != message.FinishReasonEndTurn {
		t.Errorf("FinishReason(nil, 0) = %q, want %q", got, message.FinishReasonEndTurn)
	}
	if got := FinishReason(nil, 2); got != message.FinishReasonToolUse {
		t.Errorf("FinishReason(nil, 2) = %q, want %q", got, message.FinishReasonToolUse)
	}
}

func TestAssistantPhase(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "commentary",
			raw:  `{"id":"r","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","phase":"commentary","content":[]}]}`,
			want: "commentary",
		},
		{
			name: "final_answer",
			raw:  `{"id":"r","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","phase":"final_answer","content":[]}]}`,
			want: "final_answer",
		},
		{
			name: "the last message wins",
			raw: `{"id":"r","status":"completed","output":[
				{"type":"message","id":"m1","role":"assistant","status":"completed","phase":"commentary","content":[]},
				{"type":"message","id":"m2","role":"assistant","status":"completed","phase":"final_answer","content":[]}]}`,
			want: "final_answer",
		},
		{
			name: "absent phase",
			raw:  `{"id":"r","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[]}]}`,
			want: "",
		},
		{
			name: "no message items",
			raw:  `{"id":"r","status":"completed","output":[{"type":"function_call","call_id":"c1","name":"bash","arguments":"{}"}]}`,
			want: "",
		},
		{
			name: "no output",
			raw:  `{"id":"r","status":"completed"}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AssistantPhase(decode(t, tt.raw)); got != tt.want {
				t.Errorf("AssistantPhase() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("nil response", func(t *testing.T) {
		if got := AssistantPhase(nil); got != "" {
			t.Errorf("AssistantPhase(nil) = %q, want \"\"", got)
		}
	})
}
