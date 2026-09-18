// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
)

func stringReader(s string) io.Reader { return strings.NewReader(s) }

// collect drains the driver events a body produces.
func collect(t *testing.T, body string) []llm.DriverEvent {
	t.Helper()
	client := &Client{}
	eventChan := make(chan llm.DriverEvent, 64)
	go func() {
		defer close(eventChan)
		client.consumeStream(context.Background(), stringReader(body), eventChan)
	}()
	var events []llm.DriverEvent
	for event := range eventChan {
		events = append(events, event)
	}
	return events
}

func completeEvent(t *testing.T, events []llm.DriverEvent) *llm.DriverResponse {
	t.Helper()
	for _, event := range events {
		if event.Type == llm.EventError {
			t.Fatalf("unexpected error event: %v", event.Error)
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == llm.EventComplete {
			return events[i].Response
		}
	}
	t.Fatal("stream produced no complete event")
	return nil
}

// TestConsumeStreamCapture runs the recorded capture end to end. Before the
// double-envelope unwrap existed this produced an empty response.
func TestConsumeStreamCapture(t *testing.T) {
	events := collect(t, captureFrame)
	resp := completeEvent(t, events)

	if want := "Hello again! What would you like to work on today?"; resp.Content != want {
		t.Errorf("content = %q, want %q", resp.Content, want)
	}
	if resp.FinishReason != message.FinishReasonEndTurn {
		t.Errorf("finish reason = %q, want end_turn", resp.FinishReason)
	}
	// Usage comes from the LAST frame seen.
	if resp.Usage.TokenCount != 13358 {
		t.Errorf("total tokens = %d, want 13358", resp.Usage.TokenCount)
	}
	if resp.Usage.InputTokens != 13318 || resp.Usage.OutputTokens != 12 {
		t.Errorf("usage = %+v, want input 13318 / output 12", resp.Usage)
	}
	// The final frame's signature rides on a text part with empty text.
	if resp.ThinkingSignature != "EvEBCu4BAWkUfRNbbvBlFHw" {
		t.Errorf("thinking signature = %q, want the capture's value", resp.ThinkingSignature)
	}

	var sawContentDelta bool
	for _, event := range events {
		if event.Type == llm.EventContentDelta {
			sawContentDelta = true
		}
	}
	if !sawContentDelta {
		t.Error("expected at least one content delta")
	}
}

// TestConsumeStreamStripsToolPrefix is the receive half of the round trip: a
// function call named reliant__foo on the wire becomes a call to foo.
func TestConsumeStreamStripsToolPrefix(t *testing.T) {
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"reliant__view_file","args":{"path":"main.go"}},"thoughtSignature":"SIG123"}` +
		`]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":42}},"traceId":"t"}

`
	events := collect(t, body)
	resp := completeEvent(t, events)

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
	if call.Name != "view_file" {
		t.Errorf("tool call name = %q, want the prefix stripped to %q", call.Name, "view_file")
	}
	if call.Input != `{"path":"main.go"}` {
		t.Errorf("tool call input = %q", call.Input)
	}
	// Stored verbatim: the wire carries base64 already, so there is no
	// decode/encode round trip to lose it in.
	if call.ThoughtSignature != "SIG123" {
		t.Errorf("thought signature = %q, want SIG123", call.ThoughtSignature)
	}
	if resp.FinishReason != message.FinishReasonToolUse {
		t.Errorf("finish reason = %q, want tool_use", resp.FinishReason)
	}
}

// TestConsumeStreamThoughtParts pins that parts flagged thought become thinking
// events rather than assistant content.
func TestConsumeStreamThoughtParts(t *testing.T) {
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"pondering","thought":true,"thoughtSignature":"TSIG"},{"text":"answer"}` +
		`]},"finishReason":"STOP"}]},"traceId":"t"}

`
	events := collect(t, body)
	resp := completeEvent(t, events)

	if resp.Thinking != "pondering" {
		t.Errorf("thinking = %q, want %q", resp.Thinking, "pondering")
	}
	if resp.Content != "answer" {
		t.Errorf("content = %q, want %q", resp.Content, "answer")
	}
	if resp.ThinkingSignature != "TSIG" {
		t.Errorf("thinking signature = %q, want TSIG", resp.ThinkingSignature)
	}
	if strings.Contains(resp.Content, "pondering") {
		t.Error("thought text leaked into assistant content")
	}
}

// TestScanFramesBothWireShapes pins that the single-line `data: {…}` form and
// the capture's pretty-printed multi-line form both parse.
func TestScanFramesBothWireShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "single line",
			body: "data: {\"response\":{\"modelVersion\":\"m\"}}\n\n",
		},
		{
			name: "pretty printed",
			body: "data:\n{\n  \"response\": {\n    \"modelVersion\": \"m\"\n  }\n}\n\n",
		},
		{
			name: "with comments and event fields",
			body: ": keepalive\nevent: message\ndata: {\"response\":{\"modelVersion\":\"m\"}}\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var frames []string
			if err := scanFrames(stringReader(tc.body), func(raw []byte) error {
				frames = append(frames, string(raw))
				return nil
			}); err != nil {
				t.Fatalf("scanFrames: %v", err)
			}
			if len(frames) != 1 {
				t.Fatalf("expected 1 frame, got %d: %v", len(frames), frames)
			}
			if !strings.Contains(frames[0], `"modelVersion"`) {
				t.Errorf("frame payload lost its content: %q", frames[0])
			}
		})
	}
}

// TestScanFramesSkipsDone pins that a terminal [DONE] sentinel is not handed to
// the JSON parser.
func TestScanFramesSkipsDone(t *testing.T) {
	var frames int
	err := scanFrames(stringReader("data: [DONE]\n\n"), func([]byte) error {
		frames++
		return nil
	})
	if err != nil {
		t.Fatalf("scanFrames: %v", err)
	}
	if frames != 0 {
		t.Errorf("[DONE] produced %d frames, want 0", frames)
	}
}

// TestConsumeStreamIgnoresUnwrappedFrame pins the defensive half of the
// envelope rule: a frame carrying a bare GenerateContentResponse (no
// "response" member) contributes nothing rather than being read as a response.
func TestConsumeStreamIgnoresUnwrappedFrame(t *testing.T) {
	body := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"leaked"}]}}]}

`
	resp := completeEvent(t, collect(t, body))
	if resp.Content != "" {
		t.Errorf("content = %q, want empty: an unwrapped frame is not a valid Antigravity response", resp.Content)
	}
}
