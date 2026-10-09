// Copyright (c) 2025 Reliant Labs
package message

import (
	"strings"
	"testing"
)

func textMsg(role MessageRole, chars int, tokenCount int64) Message {
	return Message{
		Role:       role,
		Parts:      []ContentPart{TextContent{Text: strings.Repeat("x", chars)}},
		TokenCount: tokenCount,
	}
}

func toolResultMsg(chars int) Message {
	return Message{
		Role:  Tool,
		Parts: []ContentPart{ToolResult{ToolCallID: "t", Content: strings.Repeat("x", chars)}},
	}
}

// The windows below are the shapes prod thread ee7dcaa4 (chat 416f3fe9,
// gpt-5.6-terra on codex, threshold 231,200) actually had on 2026-10-09: a
// 209-char brief whose very first turn reported 246,838 tokens, and the
// compacted windows after it, each opening at ~245.9k with a ~1k-char summary.
func TestEstimateCompactionReclaim(t *testing.T) {
	tests := []struct {
		name            string
		messages        []Message
		wantCurrent     int
		wantFloor       int
		wantReclaimable int
		wantWorthwhile  bool
	}{
		{
			name: "fresh thread whose fixed base already exceeds the threshold",
			messages: []Message{
				textMsg(User, 209, 0),
				textMsg(Assistant, 0, 246_838),
				toolResultMsg(8_707),
			},
			wantCurrent:     246_838 + 8_707/4,
			wantFloor:       246_838,
			wantReclaimable: 209/4 + 8_707/4,
			wantWorthwhile:  false,
		},
		{
			name: "compacted window that opened as full as the one it replaced",
			messages: []Message{
				textMsg(System, 999, 0), // the previous compaction's summary
				textMsg(Assistant, 0, 245_918),
				toolResultMsg(16_807),
			},
			wantCurrent:     245_918 + 16_807/4,
			wantFloor:       245_918,
			wantReclaimable: 16_807 / 4,
			wantWorthwhile:  false,
		},
		{
			name: "ordinary thread that grew past its threshold",
			messages: []Message{
				textMsg(User, 400, 0),
				textMsg(Assistant, 200, 40_000),
				toolResultMsg(4_000),
				textMsg(Assistant, 200, 231_500),
				toolResultMsg(2_000),
			},
			wantCurrent:     231_500 + 2_000/4,
			wantFloor:       40_000,
			wantReclaimable: (231_500 - 40_000) + 400/4 + 200/4 + 2_000/4,
			wantWorthwhile:  true,
		},
		{
			name: "opening prompt is the bulk of the context",
			messages: []Message{
				textMsg(User, 800_000, 0),
				textMsg(Assistant, 100, 240_000),
				toolResultMsg(1_000),
			},
			wantCurrent:     240_000 + 1_000/4,
			wantFloor:       240_000,
			wantReclaimable: 800_000/4 + 100/4 + 1_000/4,
			wantWorthwhile:  true,
		},
		{
			name: "bloated thread that has since accumulated enough to reclaim",
			messages: []Message{
				textMsg(System, 999, 0),
				textMsg(Assistant, 0, 245_918),
				toolResultMsg(4_000),
				textMsg(Assistant, 0, 275_000),
			},
			wantCurrent:     275_000,
			wantFloor:       245_918,
			wantReclaimable: 275_000 - 245_918,
			wantWorthwhile:  true,
		},
		{
			name: "no provider counts yet",
			messages: []Message{
				textMsg(System, 4_000, 0),
				textMsg(User, 8_000, 0),
			},
			wantCurrent:     1_000 + 2_000,
			wantFloor:       0,
			wantReclaimable: 2_000,
			wantWorthwhile:  true,
		},
		{
			name:           "empty window",
			wantWorthwhile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCompactionReclaim(tt.messages)
			if got.Current != tt.wantCurrent || got.Floor != tt.wantFloor || got.Reclaimable != tt.wantReclaimable {
				t.Fatalf("EstimateCompactionReclaim = %+v, want {Current:%d Floor:%d Reclaimable:%d}",
					got, tt.wantCurrent, tt.wantFloor, tt.wantReclaimable)
			}
			if got.Worthwhile() != tt.wantWorthwhile {
				t.Fatalf("Worthwhile() = %v, want %v (reclaimable %d of %d)",
					got.Worthwhile(), tt.wantWorthwhile, got.Reclaimable, got.Current)
			}
		})
	}
}

// A provider count that drops inside a window (a trimmed request reports a
// smaller context than the untrimmed turn before it) is not negative growth.
func TestEstimateCompactionReclaim_ShrinkingCountIsNotNegative(t *testing.T) {
	got := EstimateCompactionReclaim([]Message{
		textMsg(Assistant, 0, 250_000),
		textMsg(Assistant, 0, 240_000),
	})
	if got.Reclaimable != 0 {
		t.Fatalf("Reclaimable = %d, want 0", got.Reclaimable)
	}
}
