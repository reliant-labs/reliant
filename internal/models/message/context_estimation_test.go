// Copyright (c) 2025 Reliant Labs
package message

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Table-Driven Tests: EstimateFullContextTokens
// =============================================================================

// mockTool implements ToolDefinition for testing
type mockTool struct {
	name        string
	description string
	schemaJSON  []byte
}

func (m mockTool) Name() string            { return m.name }
func (m mockTool) Description() string     { return m.description }
func (m mockTool) ParamSchemaJSON() []byte { return m.schemaJSON }

func TestEstimateFullContextTokens_TableDriven(t *testing.T) {
	tests := []struct {
		name                     string
		messages                 []Message
		systemPrompts            []string
		tools                    []ToolDefinition
		expectedMessageTokens    int
		expectedSystemTokens     int
		expectedToolTokens       int
		expectedTotalTokens      int
		useTokenDataFromMessages bool // If true, messages have stored token data
	}{
		{
			name:                  "empty messages returns zero",
			messages:              []Message{},
			systemPrompts:         nil,
			tools:                 nil,
			expectedMessageTokens: 0,
			expectedTotalTokens:   0,
		},
		{
			name: "single text message estimates from chars",
			messages: []Message{
				{Parts: []ContentPart{TextContent{Text: strings.Repeat("a", 400)}}}, // 400 chars = 100 tokens
			},
			expectedMessageTokens: 100,
			expectedTotalTokens:   100,
		},
		{
			name: "multiple messages without token data",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("u", 200)}}},      // 50 tokens
				{Role: Assistant, Parts: []ContentPart{TextContent{Text: strings.Repeat("a", 400)}}}, // 100 tokens
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("u", 200)}}},      // 50 tokens
			},
			expectedMessageTokens: 200, // 50 + 100 + 50
			expectedTotalTokens:   200,
		},
		{
			name: "message with stored token data uses TokenCount directly",
			messages: []Message{
				{
					Role:       Assistant,
					Parts:      []ContentPart{TextContent{Text: "response"}},
					TokenCount: 15300,
				},
			},
			useTokenDataFromMessages: true,
			expectedMessageTokens:    15300,
			expectedTotalTokens:      15300,
		},
		{
			name: "messages after token data are char-estimated, messages before are skipped",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("x", 4000)}}}, // Would be 1000 tokens if counted
				{
					Role:       Assistant,
					Parts:      []ContentPart{TextContent{Text: "response"}},
					TokenCount: 3150,
				},
				{Role: Tool, Parts: []ContentPart{ToolResult{Content: strings.Repeat("r", 800)}}}, // 200 tokens after token data
			},
			useTokenDataFromMessages: true,
			expectedMessageTokens:    3350, // 200 (tool result) + 3150 TokenCount
			expectedTotalTokens:      3350,
		},
		{
			name: "system prompts added when no token data",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 400)}}}, // 100 tokens
			},
			systemPrompts: []string{
				strings.Repeat("s", 200), // 50 tokens
				strings.Repeat("s", 200), // 50 tokens
			},
			expectedMessageTokens: 100,
			expectedSystemTokens:  100,
			expectedTotalTokens:   200,
		},
		{
			name: "system prompts NOT added when token data exists",
			messages: []Message{
				{
					Role:       Assistant,
					Parts:      []ContentPart{TextContent{Text: "hi"}},
					TokenCount: 1000, // Token data exists
				},
			},
			systemPrompts: []string{
				strings.Repeat("s", 4000), // Would be 1000 tokens if counted
			},
			useTokenDataFromMessages: true,
			expectedMessageTokens:    1000,
			expectedSystemTokens:     0, // NOT counted because token data exists
			expectedTotalTokens:      1000,
		},
		{
			name: "tool definitions added when no token data",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 400)}}}, // 100 tokens
			},
			tools: []ToolDefinition{
				mockTool{
					name:        "my_tool", // 7 chars
					description: strings.Repeat("d", 100),
					schemaJSON:  []byte(strings.Repeat("j", 100)),
				},
			},
			expectedMessageTokens: 100,
			expectedToolTokens:    51, // (7 + 100 + 100) / 4 = 51
			expectedTotalTokens:   151,
		},
		{
			name: "tool definitions NOT added when token data exists",
			messages: []Message{
				{
					Role:       Assistant,
					Parts:      []ContentPart{TextContent{Text: "result"}},
					TokenCount: 500,
				},
			},
			tools: []ToolDefinition{
				mockTool{
					name:        "tool1",
					description: strings.Repeat("d", 1000),
					schemaJSON:  []byte(strings.Repeat("j", 1000)),
				},
			},
			useTokenDataFromMessages: true,
			expectedMessageTokens:    500,
			expectedToolTokens:       0, // NOT counted because token data exists
			expectedTotalTokens:      500,
		},
		{
			name: "combined estimate with messages, prompts, and tools",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 2000)}}},      // 500 tokens
				{Role: Assistant, Parts: []ContentPart{TextContent{Text: strings.Repeat("a", 1000)}}}, // 250 tokens
			},
			systemPrompts: []string{
				strings.Repeat("s", 400), // 100 tokens
			},
			tools: []ToolDefinition{
				mockTool{
					name:        "bash",
					description: strings.Repeat("d", 200),
					schemaJSON:  []byte(strings.Repeat("j", 200)),
				},
				mockTool{
					name:        "view",
					description: strings.Repeat("d", 200),
					schemaJSON:  []byte(strings.Repeat("j", 200)),
				},
			},
			expectedMessageTokens: 750,
			expectedSystemTokens:  100,
			expectedToolTokens:    202, // (4+200+200 + 4+200+200) / 4 = 202
			expectedTotalTokens:   1052,
		},
		{
			name: "tool result content estimated correctly",
			messages: []Message{
				{Role: Tool, Parts: []ContentPart{
					ToolResult{ToolCallID: "tc_1", Content: strings.Repeat("output", 100)}, // 600 chars = 150 tokens
				}},
			},
			expectedMessageTokens: 150,
			expectedTotalTokens:   150,
		},
		{
			name: "tool call estimated correctly",
			messages: []Message{
				{Role: Assistant, Parts: []ContentPart{
					ToolCall{ID: "tc_1", Name: "bash", Input: strings.Repeat("cmd", 100)}, // 4 + 300 = 304 chars = 76 tokens
				}},
			},
			expectedMessageTokens: 76,
			expectedTotalTokens:   76,
		},
		{
			name: "reasoning content estimated correctly",
			messages: []Message{
				{Role: Assistant, Parts: []ContentPart{
					ReasoningContent{Thinking: strings.Repeat("t", 800)}, // 800 chars = 200 tokens
				}},
			},
			expectedMessageTokens: 200,
			expectedTotalTokens:   200,
		},
		{
			name: "binary content estimated with base64 overhead",
			messages: []Message{
				{Role: User, Parts: []ContentPart{
					BinaryContent{MIMEType: "image/png", Data: make([]byte, 300)}, // 300 * 4/3 / 4 = 100 tokens
				}},
			},
			expectedMessageTokens: 100,
			expectedTotalTokens:   100,
		},
		{
			name: "image URL content estimated from URL length",
			messages: []Message{
				{Role: User, Parts: []ContentPart{
					ImageURLContent{URL: strings.Repeat("h", 400)}, // 400 chars = 100 tokens
				}},
			},
			expectedMessageTokens: 100,
			expectedTotalTokens:   100,
		},
		{
			name: "mixed content parts summed correctly",
			messages: []Message{
				{Role: Assistant, Parts: []ContentPart{
					TextContent{Text: strings.Repeat("a", 200)},                        // 50 tokens
					ToolCall{ID: "tc_1", Name: "bash", Input: strings.Repeat("i", 96)}, // (4+96)/4 = 25 tokens
				}},
				{Role: Tool, Parts: []ContentPart{
					ToolResult{ToolCallID: "tc_1", Content: strings.Repeat("o", 100)}, // 25 tokens
				}},
			},
			expectedMessageTokens: 100, // 50 + 25 + 25
			expectedTotalTokens:   100,
		},
		{
			name: "tool with nil schema JSON",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 400)}}}, // 100 tokens
			},
			tools: []ToolDefinition{
				mockTool{
					name:        "simple_tool",
					description: strings.Repeat("d", 100),
					schemaJSON:  nil, // No schema
				},
			},
			expectedMessageTokens: 100,
			expectedToolTokens:    27, // (11 + 100) / 4 = 27.75 → 27
			expectedTotalTokens:   127,
		},
		{
			name: "zero TokenCount does not count as token data",
			messages: []Message{
				{
					Role:       Assistant,
					Parts:      []ContentPart{TextContent{Text: "hi"}},
					TokenCount: 0,
				},
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("x", 400)}}}, // 100 tokens estimated
			},
			// With TokenCount=0, hasTokenData returns false, so char estimation is used
			expectedMessageTokens: 100, // Only user message counted (0 from assistant)
			expectedTotalTokens:   100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			estimate := EstimateFullContextTokens(tt.messages, tt.systemPrompts, tt.tools)

			assert.Equal(t, tt.expectedMessageTokens, estimate.MessageTokens,
				"MessageTokens: expected %d, got %d", tt.expectedMessageTokens, estimate.MessageTokens)

			if tt.expectedSystemTokens > 0 || tt.systemPrompts != nil {
				assert.Equal(t, tt.expectedSystemTokens, estimate.SystemPromptTokens,
					"SystemPromptTokens: expected %d, got %d", tt.expectedSystemTokens, estimate.SystemPromptTokens)
			}

			if tt.expectedToolTokens > 0 || tt.tools != nil {
				assert.Equal(t, tt.expectedToolTokens, estimate.ToolTokens,
					"ToolTokens: expected %d, got %d", tt.expectedToolTokens, estimate.ToolTokens)
			}

			assert.Equal(t, tt.expectedTotalTokens, estimate.TotalTokens,
				"TotalTokens: expected %d, got %d", tt.expectedTotalTokens, estimate.TotalTokens)
		})
	}
}

// =============================================================================
// Table-Driven Tests: TrimMessagesToFitContextWithFullEstimate
// =============================================================================

func TestTrimMessagesToFitContextWithFullEstimate_TableDriven(t *testing.T) {
	tests := []struct {
		name             string
		messages         []Message
		systemPrompts    []string
		tools            []ToolDefinition
		expectTrimmed    bool
		validateTrimming func(t *testing.T, messages []Message)
	}{
		{
			name: "small messages not trimmed",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: "hello"}}},
				{Role: Assistant, Parts: []ContentPart{TextContent{Text: "world"}}},
			},
			expectTrimmed: false,
		},
		{
			name:          "empty messages not trimmed",
			messages:      []Message{},
			expectTrimmed: false,
		},
		{
			name: "large last message trimmed",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: "small"}}},
				{Role: Tool, Parts: []ContentPart{
					ToolResult{ToolCallID: "tc_1", Name: "view", Content: strings.Repeat("x", 800000)}, // ~200k tokens
				}},
			},
			expectTrimmed: true,
			validateTrimming: func(t *testing.T, messages []Message) {
				toolResult := messages[1].Parts[0].(ToolResult)
				assert.Less(t, len(toolResult.Content), 800000, "Content should be trimmed")
				assert.Contains(t, toolResult.Content, TrimmedContentSuffix, "Should contain trim suffix")
			},
		},
		{
			name: "large text content trimmed",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("y", 800000)}}},
			},
			expectTrimmed: true,
			validateTrimming: func(t *testing.T, messages []Message) {
				text := messages[0].Parts[0].(TextContent)
				assert.Less(t, len(text.Text), 800000, "Text should be trimmed")
				assert.Contains(t, text.Text, TrimmedContentSuffix)
			},
		},
		{
			name: "system prompts push over limit triggers trimming",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 750000)}}}, // ~187k tokens
			},
			systemPrompts: []string{
				strings.Repeat("s", 50000), // ~12.5k tokens, pushes over 195k limit
			},
			expectTrimmed: true,
		},
		{
			name: "tool definitions push over limit triggers trimming",
			messages: []Message{
				{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("m", 750000)}}}, // ~187k tokens
			},
			tools: []ToolDefinition{
				mockTool{name: "t1", description: strings.Repeat("d", 20000), schemaJSON: []byte(strings.Repeat("j", 30000))},
				mockTool{name: "t2", description: strings.Repeat("d", 20000), schemaJSON: []byte(strings.Repeat("j", 30000))},
			}, // ~50k chars = ~12.5k tokens, pushes over limit
			expectTrimmed: true,
		},
		{
			name: "large earlier tool result gets trimmed when last message small",
			messages: []Message{
				{Role: Tool, Parts: []ContentPart{
					ToolResult{ToolCallID: "tc_1", Name: "view", Content: strings.Repeat("huge", 200000)}, // ~200k tokens
				}},
				{Role: Assistant, Parts: []ContentPart{TextContent{Text: "tiny response"}}},
			},
			expectTrimmed: true,
			validateTrimming: func(t *testing.T, messages []Message) {
				toolResult := messages[0].Parts[0].(ToolResult)
				assert.Less(t, len(toolResult.Content), 800000, "Earlier tool result should be trimmed")
			},
		},
		{
			name: "reasoning content can be trimmed",
			messages: []Message{
				{Role: Assistant, Parts: []ContentPart{
					ReasoningContent{Thinking: strings.Repeat("t", 800000)},
				}},
			},
			expectTrimmed: true,
			validateTrimming: func(t *testing.T, messages []Message) {
				reasoning := messages[0].Parts[0].(ReasoningContent)
				assert.Less(t, len(reasoning.Thinking), 800000)
				assert.Contains(t, reasoning.Thinking, TrimmedContentSuffix)
			},
		},
		{
			name: "preserves head and tail when trimming",
			messages: []Message{
				{Role: Tool, Parts: []ContentPart{
					ToolResult{
						ToolCallID: "tc_1",
						Name:       "view",
						Content:    "HEAD_START" + strings.Repeat("m", 800000) + "TAIL_END",
					},
				}},
			},
			expectTrimmed: true,
			validateTrimming: func(t *testing.T, messages []Message) {
				toolResult := messages[0].Parts[0].(ToolResult)
				assert.True(t, strings.HasPrefix(toolResult.Content, "HEAD_START"),
					"Should preserve head")
				// Tail check is complex due to suffix addition
				assert.Contains(t, toolResult.Content, "TAIL_END")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy of messages for validation
			messagesCopy := make([]Message, len(tt.messages))
			for i, msg := range tt.messages {
				partsCopy := make([]ContentPart, len(msg.Parts))
				copy(partsCopy, msg.Parts)
				messagesCopy[i] = Message{
					Role:  msg.Role,
					Parts: partsCopy,
				}
			}

			trimmed := TrimMessagesToFitContextWithFullEstimate(messagesCopy, tt.systemPrompts, tt.tools)

			assert.Equal(t, tt.expectTrimmed, trimmed,
				"Expected trimmed=%v, got %v", tt.expectTrimmed, trimmed)

			if tt.validateTrimming != nil && tt.expectTrimmed {
				tt.validateTrimming(t, messagesCopy)
			}
		})
	}
}

// =============================================================================
// Table-Driven Tests: trimWithHeadTail
// =============================================================================

func TestTrimWithHeadTail_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		targetLen   int
		expectHead  string // Expected prefix
		expectTail  string // Expected suffix (before trim suffix)
		exactResult string // If set, expect exact result
	}{
		{
			name:        "short content unchanged",
			content:     "short text",
			targetLen:   100,
			exactResult: "short text",
		},
		{
			name:        "zero target length",
			content:     "any content here",
			targetLen:   0,
			exactResult: "[content trimmed]",
		},
		{
			name:        "negative target length",
			content:     "any content",
			targetLen:   -5,
			exactResult: "[content trimmed]",
		},
		{
			name:       "normal trimming preserves head",
			content:    "HEADER_" + strings.Repeat("x", 1000) + "_FOOTER",
			targetLen:  200,
			expectHead: "HEADER_",
		},
		{
			name:       "normal trimming includes ellipsis marker",
			content:    strings.Repeat("a", 500) + strings.Repeat("b", 500),
			targetLen:  200,
			expectHead: "aaa", // Some of the 'a's should be preserved
		},
		{
			name:        "target length equals content length",
			content:     "exact length",
			targetLen:   12,
			exactResult: "exact length",
		},
		{
			name:        "target just under content length",
			content:     "just under",
			targetLen:   9,
			exactResult: "[content trimmed]", // Target minus ellipsis leaves nothing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := trimWithHeadTail(tt.content, tt.targetLen)

			if tt.exactResult != "" {
				assert.Equal(t, tt.exactResult, result)
				return
			}

			if tt.expectHead != "" {
				assert.True(t, strings.HasPrefix(result, tt.expectHead),
					"Expected prefix %q, got %q...", tt.expectHead, result[:min(len(result), 50)])
			}
		})
	}
}

// =============================================================================
// Table-Driven Tests: hasTokenData
// =============================================================================

func TestHasTokenData_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		message  Message
		expected bool
	}{
		{
			name:     "zero TokenCount returns false",
			message:  Message{TokenCount: 0},
			expected: false,
		},
		{
			name:     "positive TokenCount returns true",
			message:  Message{TokenCount: 100},
			expected: true,
		},
		{
			name:     "large TokenCount returns true",
			message:  Message{TokenCount: 15000},
			expected: true,
		},
		{
			name:     "empty message returns false",
			message:  Message{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasTokenData(&tt.message)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// Table-Driven Tests: estimatePartChars
// =============================================================================

func TestEstimatePartChars_TableDriven(t *testing.T) {
	tests := []struct {
		name          string
		part          ContentPart
		expectedChars int
	}{
		{
			name:          "TextContent",
			part:          TextContent{Text: "hello world"},
			expectedChars: 11,
		},
		{
			name:          "ReasoningContent",
			part:          ReasoningContent{Thinking: "I need to think about this"},
			expectedChars: 26,
		},
		{
			name:          "ToolCall",
			part:          ToolCall{Name: "bash", Input: `{"command": "ls"}`},
			expectedChars: 21, // 4 + 17
		},
		{
			name:          "ToolResult",
			part:          ToolResult{Content: "file1.txt\nfile2.txt"},
			expectedChars: 19,
		},
		{
			name:          "BinaryContent with base64 overhead",
			part:          BinaryContent{Data: make([]byte, 300)},
			expectedChars: 400, // 300 * 4/3 = 400
		},
		{
			name:          "ImageURLContent",
			part:          ImageURLContent{URL: "https://example.com/image.png"},
			expectedChars: 29,
		},
		{
			name:          "empty TextContent",
			part:          TextContent{Text: ""},
			expectedChars: 0,
		},
		{
			name:          "empty ToolResult",
			part:          ToolResult{Content: ""},
			expectedChars: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimatePartChars(tt.part)
			assert.Equal(t, tt.expectedChars, result)
		})
	}
}
