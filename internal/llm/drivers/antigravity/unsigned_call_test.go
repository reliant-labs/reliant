// Copyright (c) 2025 Reliant Labs
//
// Pins the recovery for history that holds a functionCall with NO signature
// to replay.
//
// thought_signature_test.go pins the leaks that LOST a signature the model
// sent. This file covers calls that never had one to lose, and still have to
// be replayable to Gemini 3.x:
//
//   - a tool call made by another model (the user switched models mid-chat),
//   - a row persisted before signature capture was fixed.
//
// Gemini 3.x 400s any request whose current turn replays an unsigned first
// functionCall, so one such row wedges the conversation forever. Google's
// documented escape hatch is a dummy signature on that first call; see
// geminiwire.SkipThoughtSignatureValidator.
package antigravity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/geminiwire"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// functionCallParts returns the functionCall parts of one content, in order.
func functionCallParts(c *content) []*part {
	var calls []*part
	for _, p := range c.Parts {
		if p != nil && p.FunctionCall != nil {
			calls = append(calls, p)
		}
	}
	return calls
}

// modelContents returns every "model" content in history, in order.
func modelContents(history []*content) []*content {
	var out []*content
	for _, c := range history {
		if c != nil && c.Role == "model" {
			out = append(out, c)
		}
	}
	return out
}

// TestConvertMessages_UnsignedToolCallGetsStandIn is the wedge itself: an
// assistant turn whose tool call carries no signature (here, one made by a
// non-Gemini model) must replay with the documented dummy on its call, or the
// next request 400s with "Function call is missing a thought_signature".
func TestConvertMessages_UnsignedToolCallGetsStandIn(t *testing.T) {
	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "look at the file"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Reading it."},
			message.ToolCall{ID: "call_gpt", Name: "view", Input: `{"file_path":"README.md"}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_gpt", Name: "view", Content: "# Reliant"},
		}},
	}

	history := convertMessages(messages)

	models := modelContents(history)
	if len(models) != 1 {
		t.Fatalf("model contents = %d, want 1; history = %s", len(models), dumpParts(t, allParts(history)))
	}
	calls := functionCallParts(models[0])
	if len(calls) != 1 {
		t.Fatalf("function calls = %d, want 1", len(calls))
	}
	if got := calls[0].ThoughtSignature; got != geminiwire.SkipThoughtSignatureValidator {
		t.Errorf("unsigned function call replayed with signature %q, want the documented "+
			"stand-in %q — without it Gemini 3.x rejects every later request in this chat",
			got, geminiwire.SkipThoughtSignatureValidator)
	}
}

// TestConvertMessages_RealSignatureUntouched guards the stand-in from
// overwriting a signature the model actually produced.
func TestConvertMessages_RealSignatureUntouched(t *testing.T) {
	messages := []message.Message{{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "tc-1", Name: "shell", Input: `{"command":"ls"}`, ThoughtSignature: "REAL_SIG"},
	}}}

	calls := functionCallParts(modelContents(convertMessages(messages))[0])
	if len(calls) != 1 {
		t.Fatalf("function calls = %d, want 1", len(calls))
	}
	if got := calls[0].ThoughtSignature; got != "REAL_SIG" {
		t.Errorf("signature = %q, want REAL_SIG untouched", got)
	}
}

// TestConvertMessages_ParallelUnsignedCallsStampOnlyFirst follows the
// documented parallel-call shape: Gemini signs only the FIRST functionCall of
// a parallel batch, so the stand-in goes there and nowhere else.
func TestConvertMessages_ParallelUnsignedCallsStampOnlyFirst(t *testing.T) {
	messages := []message.Message{{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a", Name: "view", Input: `{"file_path":"a.go"}`},
		message.ToolCall{ID: "b", Name: "view", Input: `{"file_path":"b.go"}`},
		message.ToolCall{ID: "c", Name: "view", Input: `{"file_path":"c.go"}`},
	}}}

	calls := functionCallParts(modelContents(convertMessages(messages))[0])
	if len(calls) != 3 {
		t.Fatalf("function calls = %d, want 3", len(calls))
	}
	if got := calls[0].ThoughtSignature; got != geminiwire.SkipThoughtSignatureValidator {
		t.Errorf("first call signature = %q, want %q", got, geminiwire.SkipThoughtSignatureValidator)
	}
	for i, call := range calls[1:] {
		if call.ThoughtSignature != "" {
			t.Errorf("call %d signature = %q, want empty — only the first call of a "+
				"parallel batch carries a signature", i+1, call.ThoughtSignature)
		}
	}
}

// TestConvertMessages_SignedFirstCallLeavesStepUnchanged is the documented
// shape Gemini itself produces for a parallel batch (FC1+signature, FC2): the
// step is valid as-is and must replay byte-for-byte.
func TestConvertMessages_SignedFirstCallLeavesStepUnchanged(t *testing.T) {
	messages := []message.Message{{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a", Name: "view", Input: `{}`, ThoughtSignature: "FIRST_SIG"},
		message.ToolCall{ID: "b", Name: "view", Input: `{}`},
	}}}

	calls := functionCallParts(modelContents(convertMessages(messages))[0])
	if len(calls) != 2 {
		t.Fatalf("function calls = %d, want 2", len(calls))
	}
	if calls[0].ThoughtSignature != "FIRST_SIG" || calls[1].ThoughtSignature != "" {
		t.Errorf("signatures = [%q %q], want [FIRST_SIG \"\"] — a step Gemini signed "+
			"must replay unchanged", calls[0].ThoughtSignature, calls[1].ThoughtSignature)
	}
}

// TestConvertMessages_SignedLaterCallSuppressesStandIn: a step with ANY real
// signature is left alone, even when it is not on the first call. Stamping the
// first call would put a dummy beside a real signature, replaying a step the
// model never produced.
func TestConvertMessages_SignedLaterCallSuppressesStandIn(t *testing.T) {
	messages := []message.Message{{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a", Name: "view", Input: `{}`},
		message.ToolCall{ID: "b", Name: "view", Input: `{}`, ThoughtSignature: "LATER_SIG"},
	}}}

	calls := functionCallParts(modelContents(convertMessages(messages))[0])
	if len(calls) != 2 {
		t.Fatalf("function calls = %d, want 2", len(calls))
	}
	if calls[0].ThoughtSignature != "" || calls[1].ThoughtSignature != "LATER_SIG" {
		t.Errorf("signatures = [%q %q], want [\"\" LATER_SIG]",
			calls[0].ThoughtSignature, calls[1].ThoughtSignature)
	}
}

// TestConvertMessages_ReasoningSignatureIsNotACallSignature: the reasoning
// part's signature stays on the reasoning part. It neither counts as the
// call's signature (the server validates the functionCall part) nor moves onto
// the call — the docs require a signature to go back in its original part.
func TestConvertMessages_ReasoningSignatureIsNotACallSignature(t *testing.T) {
	messages := []message.Message{{Role: message.Assistant, Parts: []message.ContentPart{
		message.ReasoningContent{Thinking: "plan", Signature: "REASON_SIG"},
		message.ToolCall{ID: "a", Name: "view", Input: `{}`},
	}}}

	model := modelContents(convertMessages(messages))[0]
	var reasoning *part
	for _, p := range model.Parts {
		if p != nil && p.Thought {
			reasoning = p
		}
	}
	if reasoning == nil || reasoning.ThoughtSignature != "REASON_SIG" {
		t.Fatalf("reasoning part lost its signature; parts = %s", dumpParts(t, model.Parts))
	}
	calls := functionCallParts(model)
	if len(calls) != 1 {
		t.Fatalf("function calls = %d, want 1", len(calls))
	}
	if got := calls[0].ThoughtSignature; got != geminiwire.SkipThoughtSignatureValidator {
		t.Errorf("call signature = %q, want the stand-in %q (not the reasoning's)",
			got, geminiwire.SkipThoughtSignatureValidator)
	}
}

// TestConvertMessages_StandInPerStep: every step is validated independently,
// so a turn mixing a signed Gemini step with a later unsigned one (the
// mid-chat model switch) stamps only the unsigned step.
func TestConvertMessages_StandInPerStep(t *testing.T) {
	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "gem", Name: "view", Input: `{}`, ThoughtSignature: "GEM_SIG"},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "gem", Name: "view", Content: "ok"},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "gpt", Name: "shell", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "gpt", Name: "shell", Content: "ok"},
		}},
	}

	models := modelContents(convertMessages(messages))
	if len(models) != 2 {
		t.Fatalf("model contents = %d, want 2", len(models))
	}
	if got := functionCallParts(models[0])[0].ThoughtSignature; got != "GEM_SIG" {
		t.Errorf("signed step signature = %q, want GEM_SIG", got)
	}
	if got := functionCallParts(models[1])[0].ThoughtSignature; got != geminiwire.SkipThoughtSignatureValidator {
		t.Errorf("unsigned step signature = %q, want %q", got, geminiwire.SkipThoughtSignatureValidator)
	}
}

// TestConvertMessages_StandInNotCopiedToFunctionResponse: the stand-in is a
// replay fix for the functionCall part only. Google's REST examples never put
// a signature on a functionResponse, so the dummy must not leak there via the
// call lookup that tool results share.
func TestConvertMessages_StandInNotCopiedToFunctionResponse(t *testing.T) {
	messages := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "x", Name: "view", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "x", Name: "view", Content: "ok"},
		}},
	}

	for _, p := range allParts(convertMessages(messages)) {
		if p.FunctionResponse != nil && p.ThoughtSignature != "" {
			t.Errorf("functionResponse carries signature %q, want none", p.ThoughtSignature)
		}
	}
}

// TestBuildEnvelope_StandInReachesTheWireVerbatim checks the bytes that are
// actually POSTed. The server matches the literal string, so it has to survive
// marshalling unencoded — the failure the genai SDK's []byte field causes.
func TestBuildEnvelope_StandInReachesTheWireVerbatim(t *testing.T) {
	client := &Client{}
	client.options.Model.APIModel = "gemini-3.8-flash"
	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "x", Name: "view", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "x", Name: "view", Content: "ok"},
		}},
	}

	body, err := json.Marshal(client.buildEnvelope(messages, nil, nil))
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	want := `"thoughtSignature":"` + geminiwire.SkipThoughtSignatureValidator + `"`
	if !strings.Contains(string(body), want) {
		t.Errorf("request body does not carry %s verbatim:\n%s", want, body)
	}
}

func allParts(history []*content) []*part {
	var parts []*part
	for _, c := range history {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p != nil {
				parts = append(parts, p)
			}
		}
	}
	return parts
}
