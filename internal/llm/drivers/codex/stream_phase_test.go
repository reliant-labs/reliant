// Copyright (c) 2025 Reliant Labs
package codex

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
)

// TestStreamResponse_CapturesAssistantPhase pins the capture half of the phase
// pipeline: the `phase` on the output message of a `response.completed` has to
// survive the SDK's own event decoding and land on DriverResponse, which is
// what persists it and ultimately lets the next turn resend it.
//
// It drives the real stream (see sseResponsesServer) rather than calling
// responseswire.AssistantPhase directly, because a hand-built
// responses.Response would pass even if the stream loop never reached the
// terminal event.
func TestStreamResponse_CapturesAssistantPhase(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
		sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Checking the auth setup."}`),
		sseEvent("response.completed", `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","end_turn":false,"output":[{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"Checking the auth setup."}]}]}}`),
	)

	final := streamOnce(t, srv)

	if final.Phase != "commentary" {
		t.Errorf("Phase = %q, want %q", final.Phase, "commentary")
	}
}

// TestStreamResponse_NoPhaseStaysEmpty covers a model that reports no phase.
// Empty must mean "not reported" — defaulting it to a real phase would invent a
// label and resend it to the provider on the next turn.
func TestStreamResponse_NoPhaseStaysEmpty(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
		sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"All done."}`),
		sseEvent("response.completed", `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"All done."}]}]}}`),
	)

	final := streamOnce(t, srv)

	if final.Phase != "" {
		t.Errorf("Phase = %q, want empty for a response that reported none", final.Phase)
	}
}

// TestStreamResponse_ReportsPrematureStop pins the driver half of the
// premature-stop retry: a completed turn that ends on commentary with no tool
// call reaches DriverResponse.PrematureStop, including when the terminal event
// carries an empty output and the message only arrived via output_item.done.
func TestStreamResponse_ReportsPrematureStop(t *testing.T) {
	commentary := `{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"I'll inspect the auth setup next."}]}`
	cases := []struct {
		name   string
		events []string
		want   llm.PrematureStop
	}{
		{
			name: "commentary in the terminal output",
			events: []string{
				sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"I'll inspect the auth setup next."}`),
				sseEvent("response.completed", `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[`+commentary+`]}}`),
			},
			want: llm.PrematureStopCommentary,
		},
		{
			name: "commentary only in output_item.done",
			events: []string{
				sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"I'll inspect the auth setup next."}`),
				sseEvent("response.output_item.done", `{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":`+commentary+`}`),
				sseEvent("response.completed", `{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","status":"completed","output":[]}}`),
			},
			want: llm.PrematureStopCommentary,
		},
		{
			name: "reasoning only",
			events: []string{
				sseEvent("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]}]}}`),
			},
			want: llm.PrematureStopReasoningOnly,
		},
		{
			name: "end_turn false is a pause, not a premature stop",
			events: []string{
				sseEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"I'll inspect the auth setup next."}`),
				sseEvent("response.completed", `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","end_turn":false,"output":[`+commentary+`]}}`),
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := append([]string{sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`)}, tc.events...)
			final := streamOnce(t, sseResponsesServer(t, events...))
			if final.PrematureStop != tc.want {
				t.Errorf("PrematureStop = %q, want %q", final.PrematureStop, tc.want)
			}
		})
	}
}
