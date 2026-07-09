// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// contextManagementPresent reports whether applyClaudeCodeExtras attached the
// clear_thinking context_management edit for the given client.
func contextManagementPresent(c *ClaudeCodeClient) bool {
	params := &anthropic.MessageNewParams{}
	c.applyClaudeCodeExtras(params)
	raw, err := params.MarshalJSON()
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "clear_thinking_20251015")
}

// TestApplyClaudeCodeExtras_ClearThinkingGatedOnThinking verifies the
// clear_thinking_20251015 context-management strategy is only emitted when
// thinking is enabled or adaptive. Sending it with thinking disabled makes the
// API reject the request with a 400 invalid_request_error, which previously
// broke compaction summaries (they run with reasoning disabled).
func TestApplyClaudeCodeExtras_ClearThinkingGatedOnThinking(t *testing.T) {
	tests := []struct {
		name string
		opts llm.DriverOptions
		want bool
	}{
		{
			name: "adaptive model emits clear_thinking",
			opts: llm.DriverOptions{
				Model:     models.Model{APIModel: "claude-fable-5", ThinkingMode: "adaptive"},
				MaxTokens: 4096,
			},
			want: true,
		},
		{
			name: "budget model with reasoning enabled emits clear_thinking",
			opts: llm.DriverOptions{
				Model:           models.Model{APIModel: "claude-haiku-4-5", CanReason: true, ThinkingMode: "budget"},
				ReasoningEffort: "medium",
				MaxTokens:       4096,
			},
			want: true,
		},
		{
			name: "reasoning disabled omits clear_thinking (compaction path)",
			opts: llm.DriverOptions{
				Model:           models.Model{APIModel: "claude-haiku-4-5", CanReason: true, ThinkingMode: "budget"},
				ReasoningEffort: "disabled",
				MaxTokens:       4096,
			},
			want: false,
		},
		{
			name: "non-reasoning budget model omits clear_thinking",
			opts: llm.DriverOptions{
				Model:     models.Model{APIModel: "claude-haiku-4-5", CanReason: false, ThinkingMode: "budget"},
				MaxTokens: 4096,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClaudeCodeClient(tc.opts)
			if got := contextManagementPresent(client); got != tc.want {
				t.Fatalf("context_management present = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestApplyClaudeCodeExtras_FallbacksIndependentOfThinking ensures the fable-5
// fallbacks field is still emitted even when the clear_thinking edit is gated
// out (fable-5 is adaptive, so this mainly documents the independence).
func TestApplyClaudeCodeExtras_FableFallbacks(t *testing.T) {
	client := NewClaudeCodeClient(llm.DriverOptions{
		Model:     models.Model{APIModel: "claude-fable-5", ThinkingMode: "adaptive"},
		MaxTokens: 4096,
	})
	params := &anthropic.MessageNewParams{}
	client.applyClaudeCodeExtras(params)
	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(raw), "claude-opus-4-8") {
		t.Fatalf("expected fable-5 fallback to opus-4-8, got %s", string(raw))
	}
}