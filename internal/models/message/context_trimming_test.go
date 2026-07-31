// Copyright (c) 2025 Reliant Labs
package message

import (
	"strings"
	"testing"
)

// mockToolDef implements ToolDefinition for testing
type mockToolDef struct {
	name        string
	description string
	schemaJSON  []byte
}

func (m mockToolDef) Name() string            { return m.name }
func (m mockToolDef) Description() string     { return m.description }
func (m mockToolDef) ParamSchemaJSON() []byte { return m.schemaJSON }

func TestEstimateFullContextTokens(t *testing.T) {
	// Test that system prompts and tools are included in the estimate
	// when no message has token data (fallback to char estimation)
	messages := []Message{
		{Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 400)}}}, // 400 chars = 100 tokens
	}
	systemPrompts := []string{
		strings.Repeat("s", 200), // 200 chars = 50 tokens
	}
	tools := []ToolDefinition{
		mockToolDef{
			name:        "test_tool",
			description: strings.Repeat("d", 100),         // 100 chars
			schemaJSON:  []byte(strings.Repeat("j", 100)), // 100 chars
		},
		// Total tool: 10 (name) + 100 (desc) + 100 (schema) = 210 chars = 52 tokens
	}

	estimate := EstimateFullContextTokens(messages, systemPrompts, tools)

	if estimate.MessageTokens != 100 {
		t.Errorf("MessageTokens = %d, want 100", estimate.MessageTokens)
	}
	if estimate.SystemPromptTokens != 50 {
		t.Errorf("SystemPromptTokens = %d, want 50", estimate.SystemPromptTokens)
	}
	// Tool tokens: (10 + 100 + 100) / 4 = 52
	if estimate.ToolTokens != 52 {
		t.Errorf("ToolTokens = %d, want 52", estimate.ToolTokens)
	}
	if estimate.TotalTokens != 202 {
		t.Errorf("TotalTokens = %d, want 202", estimate.TotalTokens)
	}
}

func TestEstimateFullContextTokens_WithTokenData(t *testing.T) {
	// Test that when a message has token data, we use that instead of char estimation
	// and don't double-count system prompts/tools (they're included in cached tokens)
	messages := []Message{
		// First message - user message, no token data
		{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: "Hello"}},
		},
		// Second message - assistant with token data (total tokens for this turn)
		{
			Role:       Assistant,
			Parts:      []ContentPart{TextContent{Text: "Response"}},
			TokenCount: 60600, // total tokens (prompt + response + context)
		},
		// Third message - tool result after the assistant (no token data yet)
		{
			Role:  Tool,
			Parts: []ContentPart{ToolResult{Content: strings.Repeat("x", 4000)}}, // 4000 chars = 1000 tokens
		},
	}

	// System prompts and tools should NOT be added since token data includes them
	systemPrompts := []string{strings.Repeat("s", 10000)} // would be 2500 tokens if counted
	tools := []ToolDefinition{
		mockToolDef{name: "tool", description: strings.Repeat("d", 10000)}, // would be ~2500 tokens if counted
	}

	estimate := EstimateFullContextTokens(messages, systemPrompts, tools)

	// Expected:
	// - Third message (tool result): 4000 chars / 4 = 1000 tokens (char estimate)
	// - Second message (assistant): 60600 tokens (from TokenCount)
	// - Stop iterating (found token data)
	// Total: 1000 + 60600 = 61600
	expectedTokens := 1000 + 60600

	if estimate.MessageTokens != expectedTokens {
		t.Errorf("MessageTokens = %d, want %d", estimate.MessageTokens, expectedTokens)
	}

	// System prompts and tools should be 0 since we found token data
	if estimate.SystemPromptTokens != 0 {
		t.Errorf("SystemPromptTokens = %d, want 0 (should be included in cached tokens)", estimate.SystemPromptTokens)
	}
	if estimate.ToolTokens != 0 {
		t.Errorf("ToolTokens = %d, want 0 (should be included in cached tokens)", estimate.ToolTokens)
	}
	if estimate.TotalTokens != expectedTokens {
		t.Errorf("TotalTokens = %d, want %d", estimate.TotalTokens, expectedTokens)
	}
}

func TestEstimateFullContextTokens_TokenDataAtEnd(t *testing.T) {
	// Test when the last message has token data - should just use that
	messages := []Message{
		{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: strings.Repeat("x", 10000)}}, // would be 2500 tokens if counted
		},
		{
			Role:       Assistant,
			Parts:      []ContentPart{TextContent{Text: "Response"}},
			TokenCount: 95250,
		},
	}

	estimate := EstimateFullContextTokens(messages, nil, nil)

	// Should only count the assistant message's TokenCount (no char estimation needed)
	expectedTokens := 95250

	if estimate.MessageTokens != expectedTokens {
		t.Errorf("MessageTokens = %d, want %d", estimate.MessageTokens, expectedTokens)
	}
}

func TestTrimMessagesToFitContextWithFullEstimate_NoTrimNeeded(t *testing.T) {
	// Small messages should not be trimmed
	messages := []Message{
		{Parts: []ContentPart{TextContent{Text: "hello world"}}},
	}

	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)
	if trimmed {
		t.Error("Expected no trimming for small messages")
	}
}

func TestTrimMessagesToFitContextWithFullEstimate_TrimsLastMessage(t *testing.T) {
	// Create messages that exceed the safe limit
	// SafeContextTokens = 195000, CharsPerToken = 4
	// So we need > 195000 * 4 = 780000 characters

	smallContent := "This is a small earlier message"
	largeContent := strings.Repeat("x", 800000) // ~200k tokens

	messages := []Message{
		{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: smallContent}},
		},
		{
			Role:  Tool,
			Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "bash", Content: largeContent}},
		},
	}

	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)
	if !trimmed {
		t.Error("Expected trimming for large messages")
	}

	// Check that only the LAST message was trimmed
	firstMsgText := messages[0].Parts[0].(TextContent).Text
	if firstMsgText != smallContent {
		t.Error("First message should not have been trimmed")
	}

	// Check that the last message (tool result) was trimmed
	toolResult, ok := messages[1].Parts[0].(ToolResult)
	if !ok {
		t.Fatal("Expected tool result part")
	}

	if len(toolResult.Content) >= len(largeContent) {
		t.Errorf("Expected content to be trimmed, got %d chars (original %d)",
			len(toolResult.Content), len(largeContent))
	}

	// Check for the trimmed suffix
	if !strings.Contains(toolResult.Content, TrimmedContentSuffix) {
		t.Error("Expected trimmed content suffix")
	}
}

func TestDeriveSafeContextTokens(t *testing.T) {
	tests := []struct {
		name          string
		contextWindow int64
		want          int
	}{
		{name: "unknown window falls back to legacy fixed threshold", contextWindow: 0, want: SafeContextTokens},
		{name: "negative window falls back to legacy fixed threshold", contextWindow: -1, want: SafeContextTokens},
		{name: "1M window backstop is 95%", contextWindow: 1_000_000, want: 950_000},
		{name: "200k window backstop is 95%", contextWindow: 200_000, want: 190_000},
		{name: "400k window backstop is 95%", contextWindow: 400_000, want: 380_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveSafeContextTokens(tc.contextWindow); got != tc.want {
				t.Errorf("deriveSafeContextTokens(%d) = %d, want %d", tc.contextWindow, got, tc.want)
			}
		})
	}
}

// TestTrimMessagesToFitContextWindow_ModelAwareBackstop is the regression test
// for the forge-one-shot degradation: a ~400k-token context on a 1M-window model
// must NOT be trimmed, because the trim backstop (95% of 1M = 950k) sits well
// above it. The legacy model-unaware path (window=0, fixed 195k) would trim the
// same context, which is exactly the premature head/tail-shredding the model
// reported as "session severely degraded".
func TestTrimMessagesToFitContextWindow_ModelAwareBackstop(t *testing.T) {
	// ~1.6M chars / 4 = ~400k tokens — below the 950k backstop of a 1M window,
	// but above the legacy 195k fixed threshold.
	largeContent := strings.Repeat("x", 1_600_000)
	newMessages := func() []Message {
		return []Message{
			{Role: User, Parts: []ContentPart{TextContent{Text: "earlier message"}}},
			{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "bash", Content: largeContent}}},
		}
	}

	// Model-aware: 1M window → backstop 950k → 400k context is NOT trimmed.
	if trimmed := TrimMessagesToFitContextWindow(newMessages(), nil, nil, 1_000_000); trimmed {
		t.Error("1M-window model should NOT trim a ~400k-token context (compaction, at 85%, is the primary mechanism)")
	}

	// Legacy model-unaware path (window unknown) trims the same context at 195k —
	// the pre-fix behavior we are moving away from.
	if trimmed := TrimMessagesToFitContextWindow(newMessages(), nil, nil, 0); !trimmed {
		t.Error("legacy fixed-threshold path should still trim a ~400k-token context")
	}

	// A small window (200k) DOES engage the backstop for the same oversized
	// context, confirming the backstop still protects small-window models.
	if trimmed := TrimMessagesToFitContextWindow(newMessages(), nil, nil, 200_000); !trimmed {
		t.Error("200k-window model should trim a ~400k-token context (exceeds 190k backstop)")
	}
}

func TestTrimMessagesToFitContextWithFullEstimate_PreservesHeadAndTail(t *testing.T) {
	// Create content with recognizable head and tail
	head := "HEAD_MARKER_" + strings.Repeat("a", 1000)
	middle := strings.Repeat("m", 800000)
	tail := strings.Repeat("z", 1000) + "_TAIL_MARKER"
	largeContent := head + middle + tail

	messages := []Message{
		{
			Role: Tool,
			Parts: []ContentPart{
				ToolResult{
					ToolCallID: "tc_1",
					Name:       "view",
					Content:    largeContent,
				},
			},
		},
	}

	TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)

	toolResult := messages[0].Parts[0].(ToolResult)

	// Head should be preserved
	if !strings.HasPrefix(toolResult.Content, "HEAD_MARKER_") {
		t.Error("Expected head to be preserved")
	}

	// Tail should be preserved
	if !strings.HasSuffix(toolResult.Content, "_TAIL_MARKER"+TrimmedContentSuffix) {
		t.Errorf("Expected tail to be preserved, got suffix: ...%s",
			toolResult.Content[len(toolResult.Content)-100:])
	}
}

func TestTrimMessagesToFitContextWithFullEstimate_TrimsTextContent(t *testing.T) {
	// Test that text content is also trimmed (not just tool results)
	largeText := strings.Repeat("y", 800000)

	messages := []Message{
		{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: largeText}},
		},
	}

	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)
	if !trimmed {
		t.Error("Expected trimming for large text message")
	}

	textContent := messages[0].Parts[0].(TextContent)
	if len(textContent.Text) >= len(largeText) {
		t.Errorf("Expected text to be trimmed, got %d chars (original %d)",
			len(textContent.Text), len(largeText))
	}

	if !strings.Contains(textContent.Text, TrimmedContentSuffix) {
		t.Error("Expected trimmed content suffix")
	}
}

func TestTrimMessagesToFitContextWithFullEstimate_EmptyMessages(t *testing.T) {
	messages := []Message{}

	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)
	if trimmed {
		t.Error("Should not trim empty messages")
	}
}

func TestTrimMessagesToFitContextWithFullEstimate_LargeEarlierMessage(t *testing.T) {
	// First message is huge, last message is empty - should now trim earlier messages
	largeContent := strings.Repeat("x", 800000) // ~200k tokens, exceeds limit
	messages := []Message{
		{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "view", Content: largeContent}}},
		{Role: Assistant, Parts: []ContentPart{TextContent{Text: "small response"}}},
	}

	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)
	// Should trim the large tool result from earlier in conversation
	if !trimmed {
		t.Error("Should trim large earlier messages when last message is small")
	}

	// Check the earlier tool result was trimmed
	toolResult := messages[0].Parts[0].(ToolResult)
	if len(toolResult.Content) >= len(largeContent) {
		t.Errorf("Expected earlier tool result to be trimmed, got %d chars (original %d)",
			len(toolResult.Content), len(largeContent))
	}
}

func TestTrimWithFullEstimate_SystemPromptsExceedLimit(t *testing.T) {
	// Test that system prompts push us over the limit
	// Create messages that are just under the limit
	messageContent := strings.Repeat("m", 750000) // ~187k tokens
	messages := []Message{
		{Parts: []ContentPart{TextContent{Text: messageContent}}},
	}

	// Without system prompts, this should not need trimming
	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, nil)
	if trimmed {
		t.Error("Messages alone should not need trimming")
	}

	// With large system prompts, this should trigger trimming
	systemPrompts := []string{
		strings.Repeat("s", 50000), // ~12.5k tokens
	}

	trimmed = TrimMessagesToFitContextWithFullEstimate(messages, systemPrompts, nil)
	if !trimmed {
		t.Error("Should trim when system prompts push context over limit")
	}
}

func TestTrimWithFullEstimate_ToolsExceedLimit(t *testing.T) {
	// Test that tool definitions push us over the limit
	messageContent := strings.Repeat("m", 750000) // ~187k tokens
	messages := []Message{
		{Parts: []ContentPart{TextContent{Text: messageContent}}},
	}

	// Create large tool definitions
	tools := []ToolDefinition{
		mockToolDef{
			name:        "tool1",
			description: strings.Repeat("d", 10000),
			schemaJSON:  []byte(strings.Repeat("j", 30000)),
		},
		mockToolDef{
			name:        "tool2",
			description: strings.Repeat("d", 10000),
			schemaJSON:  []byte(strings.Repeat("j", 30000)),
		},
	}
	// Total tool chars: 2 * (5 + 10000 + 30000) = 80010 chars = ~20k tokens

	trimmed := TrimMessagesToFitContextWithFullEstimate(messages, nil, tools)
	if !trimmed {
		t.Error("Should trim when tool definitions push context over limit")
	}
}

func TestTrimWithHeadTail(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		targetLen int
		wantHead  string
		wantTail  string
	}{
		{
			name:      "short content no trim",
			content:   "hello",
			targetLen: 100,
			wantHead:  "hello",
			wantTail:  "",
		},
		{
			name:      "zero target",
			content:   "hello world",
			targetLen: 0,
			wantHead:  "[content trimmed]",
			wantTail:  "",
		},
		{
			name:      "negative target",
			content:   "hello world",
			targetLen: -10,
			wantHead:  "[content trimmed]",
			wantTail:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimWithHeadTail(tt.content, tt.targetLen)
			if tt.wantHead != "" && !strings.HasPrefix(got, tt.wantHead) {
				t.Errorf("Expected prefix %q, got %q", tt.wantHead, got)
			}
			if tt.wantTail != "" && !strings.HasSuffix(got, tt.wantTail) {
				t.Errorf("Expected suffix %q, got %q", tt.wantTail, got)
			}
		})
	}
}
