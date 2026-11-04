// Copyright (c) 2025 Reliant Labs
package message

import (
	"strings"
	"testing"
)

func toolMediaMsg(id string, sizes ...int) Message {
	parts := make([]BinaryContent, 0, len(sizes))
	for _, n := range sizes {
		parts = append(parts, BinaryContent{MIMEType: "image/png", Data: make([]byte, n)})
	}
	return Message{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: id, Content: "Image file: " + id, BinaryParts: parts}}}
}

func resultOf(t *testing.T, m Message) ToolResult {
	t.Helper()
	trs := m.ToolResults()
	if len(trs) != 1 {
		t.Fatalf("want one tool result, got %d", len(trs))
	}
	return trs[0]
}

// The newest results keep their media; older ones lose it and say so.
func TestLimitToolResultMediaKeepsTheNewestWindow(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		toolMediaMsg("oldest", 10),
		{Role: User, Parts: []ContentPart{TextContent{Text: "next"}, BinaryContent{MIMEType: "image/png", Data: make([]byte, 999)}}},
		toolMediaMsg("middle", 10, 10),
		toolMediaMsg("newest", 10),
	}
	got := LimitToolResultMedia(msgs, 2, 1000)

	if n := len(resultOf(t, got[3]).BinaryParts); n != 1 {
		t.Errorf("newest result kept %d parts, want 1", n)
	}
	middle := resultOf(t, got[2])
	if len(middle.BinaryParts) != 1 {
		t.Errorf("middle result kept %d parts, want its newest 1", len(middle.BinaryParts))
	}
	if !strings.Contains(middle.Content, "1 attachment(s) from this tool result are no longer shown") {
		t.Errorf("middle result does not say what was dropped: %q", middle.Content)
	}
	oldest := resultOf(t, got[0])
	if oldest.BinaryParts != nil {
		t.Errorf("oldest result kept %d parts, want none", len(oldest.BinaryParts))
	}
	if !strings.HasPrefix(oldest.Content, "Image file: oldest") || !strings.Contains(oldest.Content, "Call the tool again") {
		t.Errorf("oldest result lost its text or the note: %q", oldest.Content)
	}
	// A user's own upload is not tool media and is never dropped.
	if n := len(got[1].BinaryContent()); n != 1 {
		t.Errorf("user upload count = %d, want 1", n)
	}
	// The caller's slice is not modified.
	if n := len(resultOf(t, msgs[0]).BinaryParts); n != 1 {
		t.Errorf("input mutated: oldest result now has %d parts", n)
	}
}

// Once one part does not fit, nothing older is admitted, even a part small
// enough to fit: the window is contiguous, so the model never sees a stale
// screen without the newer one.
func TestLimitToolResultMediaStopsAtTheFirstMiss(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		toolMediaMsg("small-old", 1),
		toolMediaMsg("big", 900),
		toolMediaMsg("new", 200),
	}
	got := LimitToolResultMedia(msgs, 10, 1000)
	if n := len(resultOf(t, got[2]).BinaryParts); n != 1 {
		t.Errorf("new kept %d, want 1", n)
	}
	if n := len(resultOf(t, got[1]).BinaryParts); n != 0 {
		t.Errorf("big kept %d, want 0 (over the byte budget)", n)
	}
	if n := len(resultOf(t, got[0]).BinaryParts); n != 0 {
		t.Errorf("small-old kept %d, want 0 (older than a dropped part)", n)
	}
}

// Within budget nothing changes, and the same slice comes back.
func TestLimitToolResultMediaUnderBudgetIsUnchanged(t *testing.T) {
	t.Parallel()

	msgs := []Message{toolMediaMsg("a", 5), toolMediaMsg("b", 5)}
	got := LimitToolResultMedia(msgs, MaxReplayedToolMediaParts, MaxReplayedToolMediaBytes)
	if &got[0] != &msgs[0] {
		t.Error("under budget, want the input slice back unchanged")
	}
	for i := range got {
		if r := resultOf(t, got[i]); len(r.BinaryParts) != 1 || strings.Contains(r.Content, "no longer shown") {
			t.Errorf("message %d changed: %+v", i, r)
		}
	}
}
