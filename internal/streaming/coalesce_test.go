package streaming

import "testing"

// The coalescer must merge ONLY additive content-text deltas that target the
// same thread+block, and must never merge structural deltas — losing or
// reordering one of those corrupts the rendered message (orphaned tool calls,
// stuck placeholders). These tests pin that policy on the pure helpers.

func contentDelta(thread string, block int, text string) StreamingDelta {
	return StreamingDelta{
		DeltaType:  DeltaTypeContentBlockDelta,
		Thread:     thread,
		BlockIndex: block,
		Delta:      text,
	}
}

func TestCoalescible(t *testing.T) {
	tests := []struct {
		name  string
		delta StreamingDelta
		want  bool
	}{
		{"plain content text", contentDelta("t", 0, "hi"), true},
		{"empty content text still coalescible", contentDelta("t", 0, ""), true},
		{
			name:  "content delta carrying a tool call is NOT coalescible",
			delta: StreamingDelta{DeltaType: DeltaTypeContentBlockDelta, ToolCall: &StreamingToolCall{ID: "x"}},
			want:  false,
		},
		{
			name:  "content delta carrying a thinking signature is NOT coalescible",
			delta: StreamingDelta{DeltaType: DeltaTypeContentBlockDelta, ThinkingSignature: "sig"},
			want:  false,
		},
		{"content_block_start is structural", StreamingDelta{DeltaType: DeltaTypeContentBlockStart}, false},
		{"content_block_stop is structural", StreamingDelta{DeltaType: DeltaTypeContentBlockStop}, false},
		{"tool_use is structural", StreamingDelta{DeltaType: DeltaTypeToolUse}, false},
		{"tool_cancelled is structural", StreamingDelta{DeltaType: DeltaTypeToolCancelled}, false},
		{"stream_cancelled is structural", StreamingDelta{DeltaType: DeltaTypeStreamCancelled}, false},
		{"message_stop is structural", StreamingDelta{DeltaType: DeltaTypeMessageStop}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.delta.coalescible(); got != tt.want {
				t.Errorf("coalescible() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanCoalesceWith(t *testing.T) {
	tests := []struct {
		name string
		a, b StreamingDelta
		want bool
	}{
		{
			name: "same thread and block",
			a:    contentDelta("t", 0, "foo"),
			b:    contentDelta("t", 0, "bar"),
			want: true,
		},
		{
			name: "different block must not merge",
			a:    contentDelta("t", 0, "foo"),
			b:    contentDelta("t", 1, "bar"),
			want: false,
		},
		{
			name: "different thread must not merge",
			a:    contentDelta("t1", 0, "foo"),
			b:    contentDelta("t2", 0, "bar"),
			want: false,
		},
		{
			name: "structural neighbour must not merge",
			a:    contentDelta("t", 0, "foo"),
			b:    StreamingDelta{DeltaType: DeltaTypeContentBlockStop, Thread: "t", BlockIndex: 0},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.canCoalesceWith(tt.b); got != tt.want {
				t.Errorf("canCoalesceWith() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCoalesce_ConcatenatesText(t *testing.T) {
	a := contentDelta("t", 0, "Hello, ")
	b := contentDelta("t", 0, "world")

	got := a.coalesce(b)

	if got.Delta != "Hello, world" {
		t.Errorf("Delta = %q, want %q", got.Delta, "Hello, world")
	}
	// Identity fields must be preserved from the base delta.
	if got.Thread != "t" || got.BlockIndex != 0 || got.DeltaType != DeltaTypeContentBlockDelta {
		t.Errorf("coalesce mutated identity fields: %+v", got)
	}
	// Inputs must not be mutated (value receiver, but guard against future regressions).
	if a.Delta != "Hello, " || b.Delta != "world" {
		t.Errorf("coalesce mutated its inputs: a=%q b=%q", a.Delta, b.Delta)
	}
}

func TestCoalesce_CarriesForwardTokenCount(t *testing.T) {
	a := contentDelta("t", 0, "a")
	b := contentDelta("t", 0, "b")
	b.TokenCount = 42

	got := a.coalesce(b)

	if got.TokenCount != 42 {
		t.Errorf("TokenCount = %d, want 42 (newer delta's count carried forward)", got.TokenCount)
	}
}

func TestCoalesce_KeepsExistingTokenCountWhenNewerHasNone(t *testing.T) {
	a := contentDelta("t", 0, "a")
	a.TokenCount = 7
	b := contentDelta("t", 0, "b") // no token count

	got := a.coalesce(b)

	if got.TokenCount != 7 {
		t.Errorf("TokenCount = %d, want 7 (must not clobber with zero)", got.TokenCount)
	}
}

// A long run of same-block content deltas folds into one, mirroring what the
// consume loop does while a consumer is behind.
func TestCoalesce_FoldsRun(t *testing.T) {
	acc := contentDelta("t", 0, "")
	for _, chunk := range []string{"The ", "quick ", "brown ", "fox"} {
		next := contentDelta("t", 0, chunk)
		if !acc.canCoalesceWith(next) {
			t.Fatalf("expected %q to be coalescible with accumulator", chunk)
		}
		acc = acc.coalesce(next)
	}
	if acc.Delta != "The quick brown fox" {
		t.Errorf("folded run = %q, want %q", acc.Delta, "The quick brown fox")
	}
}
