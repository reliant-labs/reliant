// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/llm"
)

func decodeMessage(t *testing.T, raw string) responses.ResponseOutputMessage {
	t.Helper()
	var msg responses.ResponseOutputMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	return msg
}

const (
	commentaryMsg = `{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"I'll inspect the auth setup next."}]}`
	finalMsg      = `{"type":"message","id":"msg_2","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"Done."}]}`
	noPhaseMsg    = `{"type":"message","id":"msg_3","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done."}]}`
	reasoningItem = `{"type":"reasoning","id":"rs_1","summary":[]}`
)

func TestPrematureStop(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		streamed  []string
		toolCalls int
		text      string
		want      llm.PrematureStop
	}{
		{
			name: "commentary with no final answer",
			raw:  `{"id":"r","status":"completed","output":[` + commentaryMsg + `]}`,
			text: "I'll inspect the auth setup next.",
			want: llm.PrematureStopCommentary,
		},
		{
			name: "commentary then final answer is a finished turn",
			raw:  `{"id":"r","status":"completed","output":[` + commentaryMsg + `,` + finalMsg + `]}`,
			text: "I'll inspect the auth setup next.Done.",
			want: "",
		},
		{
			name: "final answer only",
			raw:  `{"id":"r","status":"completed","output":[` + finalMsg + `]}`,
			text: "Done.",
			want: "",
		},
		{
			name: "no phase reported is the provider's verdict",
			raw:  `{"id":"r","status":"completed","output":[` + noPhaseMsg + `]}`,
			text: "Done.",
			want: "",
		},
		{
			name: "reasoning only",
			raw:  `{"id":"r","status":"completed","output":[` + reasoningItem + `]}`,
			want: llm.PrematureStopReasoningOnly,
		},
		{
			name: "empty output and no text",
			raw:  `{"id":"r","status":"completed","output":[]}`,
			want: llm.PrematureStopReasoningOnly,
		},
		{
			name:     "empty terminal output reads the streamed messages",
			raw:      `{"id":"r","status":"completed","output":[]}`,
			streamed: []string{commentaryMsg},
			text:     "I'll inspect the auth setup next.",
			want:     llm.PrematureStopCommentary,
		},
		{
			name: "empty terminal output with streamed text is not reasoning-only",
			raw:  `{"id":"r","status":"completed","output":[]}`,
			text: "Done.",
			want: "",
		},
		{
			name:      "tool calls are progress",
			raw:       `{"id":"r","status":"completed","output":[` + commentaryMsg + `]}`,
			toolCalls: 1,
			text:      "I'll inspect the auth setup next.",
			want:      "",
		},
		{
			name: "end_turn false is already a pause",
			raw:  `{"id":"r","status":"completed","end_turn":false,"output":[` + commentaryMsg + `]}`,
			text: "I'll inspect the auth setup next.",
			want: "",
		},
		{
			name: "end_turn true still counts",
			raw:  `{"id":"r","status":"completed","end_turn":true,"output":[` + commentaryMsg + `]}`,
			text: "I'll inspect the auth setup next.",
			want: llm.PrematureStopCommentary,
		},
		{
			name: "incomplete is not ours",
			raw:  `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + reasoningItem + `]}`,
			want: "",
		},
		{
			name: "failed is not ours",
			raw:  `{"id":"r","status":"failed","output":[]}`,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var streamed []responses.ResponseOutputMessage
			for _, raw := range tc.streamed {
				streamed = append(streamed, decodeMessage(t, raw))
			}
			got := PrematureStop(decode(t, tc.raw), streamed, tc.toolCalls, tc.text)
			if got != tc.want {
				t.Fatalf("PrematureStop = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("no terminal response", func(t *testing.T) {
		if got := PrematureStop(nil, nil, 0, ""); got != "" {
			t.Fatalf("PrematureStop(nil) = %q, want empty", got)
		}
	})
}
