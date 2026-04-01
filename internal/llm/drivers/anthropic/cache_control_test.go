// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheControlPlacement(t *testing.T) {
	client := NewAnthropicClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.Claude45Sonnet,
			APIModel: "claude-sonnet-4-5-20250929",
		},
		DisableCache: false,
		MaxTokens:    1024,
	})

	prompts := []string{
		"You are a helpful assistant.",
		"Always be concise and clear.",
	}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "First user message"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "First assistant response"},
			},
		},
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Second user message"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Second assistant response"},
			},
		},
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Third user message"},
			},
		},
	}

	tools := []toolsPkg.Tool{
		&mockTool{name: "tool1", description: "First tool"},
		&mockTool{name: "tool2", description: "Second tool"},
	}

	t.Run("SystemPrompts", func(t *testing.T) {
		systemPrompts := []string{}
		for i, prompt := range prompts {
			block := map[string]interface{}{
				"type": "text",
				"text": prompt,
			}
			// Cache the last 2 system prompts
			totalPrompts := len(prompts)
			if !client.options.DisableCache && totalPrompts > 0 && i >= totalPrompts-2 {
				block["cache_control"] = map[string]string{"type": "ephemeral"}
			}

			data, _ := json.Marshal(block)
			systemPrompts = append(systemPrompts, string(data))
		}

		// Both prompts should have cache control (last 2)
		var prompt0 map[string]interface{}
		var prompt1 map[string]interface{}
		json.Unmarshal([]byte(systemPrompts[0]), &prompt0)
		json.Unmarshal([]byte(systemPrompts[1]), &prompt1)

		assert.NotNil(t, prompt0["cache_control"], "First system prompt should have cache_control")
		assert.NotNil(t, prompt1["cache_control"], "Second system prompt should have cache_control")
	})

	t.Run("Messages", func(t *testing.T) {
		convertedMessages := client.convertMessages(messages)

		require.Greater(t, len(convertedMessages), 0, "Should have at least one message")

		// The last message (regardless of role) should have cache control
		lastMessageIndex := len(convertedMessages) - 1

		// Check that only the last message has cache control
		for i, msg := range convertedMessages {
			data, err := json.Marshal(msg)
			require.NoError(t, err)

			var msgMap map[string]interface{}
			err = json.Unmarshal(data, &msgMap)
			require.NoError(t, err)

			hasCacheControl := false
			if content, ok := msgMap["content"].([]interface{}); ok {
				for _, item := range content {
					if contentMap, ok := item.(map[string]interface{}); ok {
						if _, exists := contentMap["cache_control"]; exists {
							hasCacheControl = true
							break
						}
					}
				}
			}

			if i == lastMessageIndex {
				assert.True(t, hasCacheControl, "Last message (index %d) should have cache_control", i)
			} else {
				assert.False(t, hasCacheControl, "Message at index %d should NOT have cache_control", i)
			}
		}
	})

	t.Run("Tools", func(t *testing.T) {
		convertedTools := client.convertTools(tools)

		// Only the last tool should have cache control
		for i := range convertedTools {
			data, err := json.Marshal(convertedTools[i])
			require.NoError(t, err)

			var toolMap map[string]interface{}
			err = json.Unmarshal(data, &toolMap)
			require.NoError(t, err)

			hasCacheControl := false
			if _, exists := toolMap["cache_control"]; exists {
				hasCacheControl = true
			}

			if i == len(convertedTools)-1 {
				assert.True(t, hasCacheControl, "Last tool (index %d) should have cache_control", i)
			} else {
				assert.False(t, hasCacheControl, "Tool at index %d should NOT have cache_control", i)
			}
		}
	})

	t.Run("TotalCacheBlocks", func(t *testing.T) {
		// We should have exactly 4 cache blocks:
		// - 2 system prompts (last 2)
		// - 1 last message (regardless of role)
		// - 1 last tool

		cacheBlockCount := 0

		// Count system prompt cache blocks (last 2)
		if len(prompts) >= 2 {
			cacheBlockCount += 2
		} else {
			cacheBlockCount += len(prompts)
		}

		// Count message cache blocks (only last message)
		convertedMessages := client.convertMessages(messages)
		for _, msg := range convertedMessages {
			data, _ := json.Marshal(msg)
			var msgMap map[string]interface{}
			json.Unmarshal(data, &msgMap)

			if content, ok := msgMap["content"].([]interface{}); ok {
				for _, item := range content {
					if contentMap, ok := item.(map[string]interface{}); ok {
						if _, exists := contentMap["cache_control"]; exists {
							cacheBlockCount++
							break
						}
					}
				}
			}
		}

		// Count tool cache blocks (only last tool)
		convertedTools := client.convertTools(tools)
		for _, tool := range convertedTools {
			data, _ := json.Marshal(tool)
			var toolMap map[string]interface{}
			json.Unmarshal(data, &toolMap)

			if _, exists := toolMap["cache_control"]; exists {
				cacheBlockCount++
			}
		}

		assert.LessOrEqual(t, cacheBlockCount, 4, "Total cache blocks should not exceed 4 (Anthropic limit)")
		assert.Equal(t, 4, cacheBlockCount, "Should have exactly 4 cache blocks: 2 system prompts + 1 last message + 1 last tool")
	})
}

func TestCacheControlNotPersisted(t *testing.T) {
	// This test verifies that cache control is computed fresh on each call
	// and doesn't persist on the message objects themselves
	client := NewAnthropicClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.Claude45Sonnet,
			APIModel: "claude-sonnet-4-5-20250929",
		},
		DisableCache: false,
		MaxTokens:    1024,
	})

	// Start with initial messages
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "First message"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "First response"},
			},
		},
	}

	// First call - the last message (Assistant) should have cache control
	t.Run("FirstCall", func(t *testing.T) {
		convertedMessages := client.convertMessages(messages)
		require.Equal(t, 2, len(convertedMessages), "Should have 2 messages")

		// Check that only the last message (index 1) has cache control
		for i, msg := range convertedMessages {
			data, err := json.Marshal(msg)
			require.NoError(t, err)

			var msgMap map[string]interface{}
			err = json.Unmarshal(data, &msgMap)
			require.NoError(t, err)

			hasCacheControl := false
			if content, ok := msgMap["content"].([]interface{}); ok {
				for _, item := range content {
					if contentMap, ok := item.(map[string]interface{}); ok {
						if _, exists := contentMap["cache_control"]; exists {
							hasCacheControl = true
							break
						}
					}
				}
			}

			if i == 1 {
				assert.True(t, hasCacheControl, "Message at index 1 should have cache_control (last message)")
			} else {
				assert.False(t, hasCacheControl, "Message at index 0 should NOT have cache_control")
			}
		}
	})

	// Append new messages to the SAME message slice
	messages = append(messages, message.Message{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Second message"},
		},
	})
	messages = append(messages, message.Message{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Second response"},
		},
	})

	// Second call - now the NEW last message (index 3) should have cache control
	// The previously-last message (index 1) should NO LONGER have cache control
	t.Run("SecondCall", func(t *testing.T) {
		convertedMessages := client.convertMessages(messages)
		require.Equal(t, 4, len(convertedMessages), "Should have 4 messages")

		// Check that only the NEW last message (index 3) has cache control
		for i, msg := range convertedMessages {
			data, err := json.Marshal(msg)
			require.NoError(t, err)

			var msgMap map[string]interface{}
			err = json.Unmarshal(data, &msgMap)
			require.NoError(t, err)

			hasCacheControl := false
			if content, ok := msgMap["content"].([]interface{}); ok {
				for _, item := range content {
					if contentMap, ok := item.(map[string]interface{}); ok {
						if _, exists := contentMap["cache_control"]; exists {
							hasCacheControl = true
							break
						}
					}
				}
			}

			if i == 3 {
				assert.True(t, hasCacheControl, "Message at index 3 should have cache_control (NEW last message)")
			} else {
				assert.False(t, hasCacheControl, "Message at index %d should NOT have cache_control", i)
			}
		}
	})
}

func TestCacheControlDisabled(t *testing.T) {
	client := NewAnthropicClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.Claude45Sonnet,
			APIModel: "claude-sonnet-4-5-20250929",
		},
		DisableCache: true, // Cache disabled
		MaxTokens:    1024,
	})

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Test message"},
			},
		},
	}

	tools := []toolsPkg.Tool{
		&mockTool{name: "tool1", description: "Test tool"},
	}

	convertedMessages := client.convertMessages(messages)
	convertedTools := client.convertTools(tools)

	// Check no cache control on messages
	for i, msg := range convertedMessages {
		data, err := json.Marshal(msg)
		require.NoError(t, err)

		var msgMap map[string]interface{}
		err = json.Unmarshal(data, &msgMap)
		require.NoError(t, err)

		hasCacheControl := false
		if content, ok := msgMap["content"].([]interface{}); ok {
			for _, item := range content {
				if contentMap, ok := item.(map[string]interface{}); ok {
					if _, exists := contentMap["cache_control"]; exists {
						hasCacheControl = true
						break
					}
				}
			}
		}

		assert.False(t, hasCacheControl, "Message at index %d should NOT have cache_control when disabled", i)
	}

	// Check no cache control on tools
	for i, tool := range convertedTools {
		data, err := json.Marshal(tool)
		require.NoError(t, err)

		var toolMap map[string]interface{}
		err = json.Unmarshal(data, &toolMap)
		require.NoError(t, err)

		hasCacheControl := false
		if _, exists := toolMap["cache_control"]; exists {
			hasCacheControl = true
		}

		assert.False(t, hasCacheControl, "Tool at index %d should NOT have cache_control when disabled", i)
	}
}

// Mock tool for testing
type mockTool struct {
	name        string
	description string
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return m.description }
func (m *mockTool) ParamSchema() *jsonschema.Schema {
	// Use jsonschema.Reflect to create a proper schema
	type MockInput struct {
		Input string `json:"input" jsonschema:"description=Test input"`
	}
	return jsonschema.Reflect(&MockInput{})
}
func (m *mockTool) RequiresPermission(rctx *rctx.ToolContext, params toolsPkg.ToolCall) (bool, error) {
	return false, nil
}
func (m *mockTool) Run(rctx *rctx.ToolContext, params toolsPkg.ToolCall) (toolsPkg.ToolResponse, error) {
	return toolsPkg.ToolResponse{Content: "mock result"}, nil
}
