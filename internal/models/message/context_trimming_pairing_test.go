// Copyright (c) 2025 Reliant Labs
//
// Trimming vs. the tool-call/tool-result pairing invariant.
//
// The invariant these tests protect: a history sent to the LLM must never
// contain an assistant tool_use block without a matching tool_result. Trimming
// runs on that history immediately before the request is built
// (prepareHistoryForLLM), so if trimming could drop a part or a message it would
// be able to manufacture a violation that no repair pass downstream would see.
//
// The property asserted here is that trimming is TOPOLOGY-PRESERVING: it only
// shortens string payloads in place. Message count, part count, part types, and
// every tool identifier survive verbatim. That is what makes a tool_use/
// tool_result pair an atomic unit with respect to trimming.
package message

import (
	"strings"
	"testing"
)

// pairedConversation builds a conversation with three tool pairs, mixed part
// types, and a configurable payload size, so trimming has plenty to bite on.
func pairedConversation(payloadSize int) []Message {
	pad := func(c string) string { return strings.Repeat(c, payloadSize) }
	return []Message{
		{ID: "m1", Role: User, Parts: []ContentPart{TextContent{Text: pad("u")}}},
		{ID: "m2", Role: Assistant, Parts: []ContentPart{
			ReasoningContent{Thinking: pad("r"), Signature: "sig-1"},
			TextContent{Text: pad("a")},
			ToolCall{ID: "tc_1", Name: "bash", Input: `{"command":"ls -la"}`},
			ToolCall{ID: "tc_2", Name: "view", Input: `{"file_path":"/tmp/x"}`},
		}},
		{ID: "m3", Role: Tool, Parts: []ContentPart{
			ToolResult{ToolCallID: "tc_1", Name: "bash", Content: pad("1")},
			ToolResult{ToolCallID: "tc_2", Name: "view", Content: pad("2")},
		}},
		{ID: "m4", Role: Assistant, Parts: []ContentPart{
			TextContent{Text: pad("b")},
			ToolCall{ID: "tc_3", Name: "grep", Input: `{"pattern":"foo"}`},
		}},
		{ID: "m5", Role: Tool, Parts: []ContentPart{
			ToolResult{ToolCallID: "tc_3", Name: "grep", Content: pad("3")},
		}},
		{ID: "m6", Role: User, Parts: []ContentPart{TextContent{Text: pad("f")}}},
	}
}

// collectToolIDs returns the tool_call ids and tool_result ids present.
func collectToolIDs(msgs []Message) (calls, results map[string]string) {
	calls, results = map[string]string{}, map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch v := p.(type) {
			case ToolCall:
				calls[v.ID] = v.Input
			case ToolResult:
				results[v.ToolCallID] = v.Name
			}
		}
	}
	return calls, results
}

// TestTrimming_IsTopologyPreserving is the core pairing guarantee. Across a
// range of payload sizes and context windows — including windows small enough to
// force the most aggressive trimming path — trimming must never change the shape
// of the conversation, only the length of its strings.
//
// If this test fails, trimming has gained the ability to orphan a tool pair and
// the fix belongs here, not in a downstream repair pass.
func TestTrimming_IsTopologyPreserving(t *testing.T) {
	payloadSizes := []int{1_000, 100_000, 900_000, 5_000_000}
	contextWindows := []int64{0, 8_000, 200_000, 1_000_000}

	for _, size := range payloadSizes {
		for _, window := range contextWindows {
			msgs := pairedConversation(size)

			wantRoles := make([]MessageRole, len(msgs))
			wantPartTypes := make([][]string, len(msgs))
			for i, m := range msgs {
				wantRoles[i] = m.Role
				for _, p := range m.Parts {
					wantPartTypes[i] = append(wantPartTypes[i], partTypeName(p))
				}
			}
			wantCalls, wantResults := collectToolIDs(msgs)

			TrimMessagesToFitContextWindow(msgs, nil, nil, window)

			if len(msgs) != len(wantRoles) {
				t.Fatalf("size=%d window=%d: trimming changed message count %d -> %d (a dropped message can orphan a tool pair)",
					size, window, len(wantRoles), len(msgs))
			}
			for i, m := range msgs {
				if m.Role != wantRoles[i] {
					t.Errorf("size=%d window=%d: msg[%d] role changed %s -> %s", size, window, i, wantRoles[i], m.Role)
				}
				if len(m.Parts) != len(wantPartTypes[i]) {
					t.Fatalf("size=%d window=%d: msg[%d] part count changed %d -> %d (a dropped part can orphan a tool pair)",
						size, window, i, len(wantPartTypes[i]), len(m.Parts))
				}
				for j, p := range m.Parts {
					if got := partTypeName(p); got != wantPartTypes[i][j] {
						t.Errorf("size=%d window=%d: msg[%d].Parts[%d] type changed %s -> %s",
							size, window, i, j, wantPartTypes[i][j], got)
					}
				}
			}

			gotCalls, gotResults := collectToolIDs(msgs)
			for id, input := range wantCalls {
				if _, ok := gotCalls[id]; !ok {
					t.Errorf("size=%d window=%d: tool_call %s vanished during trimming", size, window, id)
					continue
				}
				// A truncated tool input is corrupt arguments, not a smaller
				// prompt — tool calls must pass through byte-identical.
				if gotCalls[id] != input {
					t.Errorf("size=%d window=%d: tool_call %s input was modified (%d -> %d chars)",
						size, window, id, len(input), len(gotCalls[id]))
				}
			}
			for id := range wantResults {
				if _, ok := gotResults[id]; !ok {
					t.Errorf("size=%d window=%d: tool_result for %s vanished during trimming", size, window, id)
				}
			}
		}
	}
}

// TestTrimming_NeverEmptiesToolResultContent guards the other way a result can
// stop being a valid answer: surviving as a part but with empty content.
// Providers treat an empty tool_result as malformed.
func TestTrimming_NeverEmptiesToolResultContent(t *testing.T) {
	// A single enormous result drives keepRatio to its floor.
	msgs := []Message{
		{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "bash", Input: "{}"}}},
		{Role: Tool, Parts: []ContentPart{
			ToolResult{ToolCallID: "tc_1", Name: "bash", Content: strings.Repeat("y", 40_000_000)},
		}},
	}

	TrimMessagesToFitContextWindow(msgs, nil, nil, 200_000)

	tr, ok := msgs[1].Parts[0].(ToolResult)
	if !ok {
		t.Fatalf("expected a ToolResult, got %T", msgs[1].Parts[0])
	}
	if tr.ToolCallID != "tc_1" {
		t.Errorf("ToolCallID must survive trimming verbatim, got %q", tr.ToolCallID)
	}
	if strings.TrimSpace(tr.Content) == "" {
		t.Error("trimming produced an empty tool_result body; providers reject this")
	}
}

// TestTrimming_PreservesToolResultBinaryParts is the regression test for a real
// bug this work found: both trim paths rebuilt ToolResult from an explicit field
// list that omitted BinaryParts, so any image or PDF attached to a tool result
// was silently discarded the moment the context got large enough to trim.
//
// LOAD-BEARING: against the pre-fix code both subtests fail with
// "binary parts were dropped: 0 of 1 survived".
func TestTrimming_PreservesToolResultBinaryParts(t *testing.T) {
	newResult := func() ToolResult {
		return ToolResult{
			ToolCallID:  "tc_1",
			Name:        "screenshot",
			Content:     strings.Repeat("x", 900_000),
			Metadata:    `{"width":800}`,
			IsError:     false,
			BinaryParts: []BinaryContent{{MIMEType: "image/png", Data: []byte("fake-png-bytes")}},
		}
	}

	tests := []struct {
		name     string
		messages []Message
	}{
		{
			// Trailing tool message: the trimLastMessage / trimPart path.
			name: "last message path",
			messages: []Message{
				{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "screenshot", Input: "{}"}}},
				{Role: Tool, Parts: []ContentPart{newResult()}},
			},
		},
		{
			// A later message makes the result "earlier", routing it through
			// the separate trimLargeToolResults path, which had the same bug.
			name: "large tool results path",
			messages: []Message{
				{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "screenshot", Input: "{}"}}},
				{Role: Tool, Parts: []ContentPart{newResult()}},
				{Role: User, Parts: []ContentPart{TextContent{Text: "and then?"}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			TrimMessagesToFitContextWindow(tc.messages, nil, nil, 200_000)

			tr, ok := tc.messages[1].Parts[0].(ToolResult)
			if !ok {
				t.Fatalf("expected a ToolResult, got %T", tc.messages[1].Parts[0])
			}
			if len(tr.Content) >= 900_000 {
				t.Fatal("precondition failed: content was not trimmed, so this test proves nothing")
			}
			if len(tr.BinaryParts) != 1 {
				t.Errorf("binary parts were dropped: %d of 1 survived", len(tr.BinaryParts))
			} else if string(tr.BinaryParts[0].Data) != "fake-png-bytes" {
				t.Error("binary part payload was corrupted by trimming")
			}
			if tr.Metadata != `{"width":800}` {
				t.Errorf("metadata was lost during trimming: %q", tr.Metadata)
			}
			if tr.ToolCallID != "tc_1" {
				t.Errorf("ToolCallID changed during trimming: %q", tr.ToolCallID)
			}
		})
	}
}

// partTypeName gives a stable name per ContentPart implementation.
func partTypeName(p ContentPart) string {
	switch p.(type) {
	case TextContent:
		return "text"
	case ReasoningContent:
		return "reasoning"
	case ToolCall:
		return "tool_call"
	case ToolResult:
		return "tool_result"
	case BinaryContent:
		return "binary"
	case ImageURLContent:
		return "image_url"
	case Finish:
		return "finish"
	default:
		return "unknown"
	}
}
