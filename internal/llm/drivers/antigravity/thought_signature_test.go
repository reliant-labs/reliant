// Copyright (c) 2025 Reliant Labs
//
// Pins the two halves of the "Function call is missing a thought_signature"
// 400 that Antigravity returns mid-conversation.
//
// The failure is not in the turn that errors — it is in the turn BEFORE it.
// Gemini 3.x signs a reasoning step and expects that exact signature echoed
// back on every later request that replays the step. A turn that loses its
// signature is accepted at the time (the response streams fine) and only 400s
// on the NEXT request, naming the position of the offending functionCall. That
// delay is why the bug reads as random.
//
// Two independent leaks, both reproduced here:
//
//  1. The signature arrives on a part that is neither the functionCall part
//     nor a thought part — a bare signature-only part in the same candidate.
//     consumeStream attributed it to `thinkingSignature` only, so the tool call
//     was persisted with an empty signature and replayed bare.
//
//  2. convertMessages replayed the assistant's reasoning as nothing at all. A
//     stored ReasoningContent with a signature never became a part, so a turn
//     whose signature lived on the thinking block (the common shape) lost it on
//     the way back out even when the database had it.
package antigravity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// collectEvents drains the driver events produced by consuming body.
func collectEvents(t *testing.T, body string) []llm.DriverEvent {
	t.Helper()

	client := &Client{}
	eventChan := make(chan llm.DriverEvent, 64)
	go func() {
		defer close(eventChan)
		client.consumeStream(context.Background(), strings.NewReader(body), eventChan)
	}()

	var events []llm.DriverEvent
	for event := range eventChan {
		events = append(events, event)
	}
	return events
}

func finalResponse(t *testing.T, events []llm.DriverEvent) *llm.DriverResponse {
	t.Helper()
	for _, event := range events {
		if event.Type == llm.EventComplete && event.Response != nil {
			return event.Response
		}
	}
	t.Fatal("no EventComplete with a response")
	return nil
}

// TestConsumeStream_SignatureOnSiblingPart is leak #1.
//
// The signature rides on its own part alongside the functionCall rather than
// on the functionCall part itself. This is the shape the failing conversation
// produced: the tool_call row was written with an empty thought_signature
// while a signature was present in the very same candidate.
func TestConsumeStream_SignatureOnSiblingPart(t *testing.T) {
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"reliant__shell","args":{"command":"sleep 1"}}},` +
		`{"thoughtSignature":"SIDECAR_SIG"}` +
		`]}}]}}

`

	resp := finalResponse(t, collectEvents(t, body))

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].ThoughtSignature; got != "SIDECAR_SIG" {
		t.Errorf("tool call thought signature = %q, want %q — a functionCall "+
			"replayed without its signature is what produces the 400", got, "SIDECAR_SIG")
	}
}

// TestConsumeStream_SignatureFromThoughtPartBacksToolCall covers the same leak
// when the signature arrives on a thought part that precedes the call. The
// signature belongs to the reasoning that PRODUCED the call, so the call must
// carry it on replay.
func TestConsumeStream_SignatureFromThoughtPartBacksToolCall(t *testing.T) {
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"deciding","thought":true,"thoughtSignature":"THOUGHT_SIG"},` +
		`{"functionCall":{"name":"reliant__shell","args":{"command":"ls"}}}` +
		`]}}]}}

`

	resp := finalResponse(t, collectEvents(t, body))

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].ThoughtSignature; got != "THOUGHT_SIG" {
		t.Errorf("tool call thought signature = %q, want %q", got, "THOUGHT_SIG")
	}
}

// TestConsumeStream_ExplicitSignatureWins guards the fallback from clobbering
// a signature the functionCall part carried itself.
func TestConsumeStream_ExplicitSignatureWins(t *testing.T) {
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"deciding","thought":true,"thoughtSignature":"THOUGHT_SIG"},` +
		`{"functionCall":{"name":"reliant__shell","args":{}},"thoughtSignature":"OWN_SIG"}` +
		`]}}]}}

`

	resp := finalResponse(t, collectEvents(t, body))

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].ThoughtSignature; got != "OWN_SIG" {
		t.Errorf("tool call thought signature = %q, want OWN_SIG (the part's own "+
			"signature must not be overwritten by a sibling)", got)
	}
}

// TestConvertMessages_ReplaysReasoningSignature is leak #2.
//
// The assistant turn's signature is stored on its reasoning, which is the
// common shape: in the failing conversation the THINKING block held a 968-byte
// signature while the tool_call block held none. If convertMessages drops the
// reasoning, that signature never reaches the wire and the replayed turn is
// unsigned.
func TestConvertMessages_ReplaysReasoningSignature(t *testing.T) {
	assistant := message.Message{Role: message.Assistant}
	assistant.Parts = []message.ContentPart{
		message.ReasoningContent{Thinking: "considering the request", Signature: "REASON_SIG"},
		message.TextContent{Text: "Here is the answer."},
	}

	history := convertMessages([]message.Message{assistant})

	if len(history) != 1 {
		t.Fatalf("history entries = %d, want 1", len(history))
	}

	var signed *part
	for _, p := range history[0].Parts {
		if p != nil && p.ThoughtSignature == "REASON_SIG" {
			signed = p
			break
		}
	}
	if signed == nil {
		t.Fatalf("no part carried the reasoning signature; parts = %s", dumpParts(t, history[0].Parts))
	}
	if !signed.Thought {
		t.Errorf("the signed part must be marked thought=true so the server reads "+
			"it as reasoning rather than user-visible text; parts = %s", dumpParts(t, history[0].Parts))
	}
}

// TestConvertMessages_ReasoningSignatureWithoutText covers the signature-only
// reasoning block. IsBlockValid deliberately persists these rows (a signature
// with no readable text is still what lets the next turn resume), so the
// converter must replay them rather than filter them out for having no text.
func TestConvertMessages_ReasoningSignatureWithoutText(t *testing.T) {
	assistant := message.Message{Role: message.Assistant}
	assistant.Parts = []message.ContentPart{
		message.ReasoningContent{Signature: "BARE_SIG"},
		message.ToolCall{ID: "tc-1", Name: "shell", Input: `{"command":"ls"}`},
	}

	history := convertMessages([]message.Message{assistant})

	if len(history) != 1 {
		t.Fatalf("history entries = %d, want 1", len(history))
	}
	for _, p := range history[0].Parts {
		if p != nil && p.ThoughtSignature == "BARE_SIG" {
			return
		}
	}
	t.Fatalf("signature-only reasoning was dropped; parts = %s", dumpParts(t, history[0].Parts))
}

// TestConvertMessages_ToolCallSignatureStillTravels guards the existing
// behaviour the fix must not regress.
func TestConvertMessages_ToolCallSignatureStillTravels(t *testing.T) {
	assistant := message.Message{Role: message.Assistant}
	assistant.Parts = []message.ContentPart{
		message.ToolCall{ID: "tc-1", Name: "shell", Input: `{"command":"ls"}`, ThoughtSignature: "CALL_SIG"},
	}

	history := convertMessages([]message.Message{assistant})

	if len(history) != 1 {
		t.Fatalf("history entries = %d, want 1", len(history))
	}
	for _, p := range history[0].Parts {
		if p != nil && p.FunctionCall != nil {
			if p.ThoughtSignature != "CALL_SIG" {
				t.Errorf("function call signature = %q, want CALL_SIG", p.ThoughtSignature)
			}
			return
		}
	}
	t.Fatal("no function call part emitted")
}

func dumpParts(t *testing.T, parts []*part) string {
	t.Helper()
	encoded, err := json.Marshal(parts)
	if err != nil {
		return "<unencodable>"
	}
	return string(encoded)
}
