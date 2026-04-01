// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiModelDetection(t *testing.T) {
	tests := []struct {
		name     string
		modelID  models.ModelID
		apiModel string
		expected bool
	}{
		{
			name:     "Gemini 3.1 Pro",
			modelID:  models.Gemini31ProPreview,
			apiModel: "google/gemini-3.1-pro-preview",
			expected: true,
		},
		{
			name:     "Gemini 3 Flash",
			modelID:  models.Gemini3FlashPreview,
			apiModel: "google/gemini-3-flash-preview",
			expected: true,
		},
		{
			name:     "Gemini 2.5 Pro",
			modelID:  models.Gemini25Pro,
			apiModel: "google/gemini-2.5-pro",
			expected: true,
		},
		{
			name:     "Claude Sonnet",
			modelID:  models.Claude45Sonnet,
			apiModel: "anthropic/claude-4.5-sonnet",
			expected: false,
		},
		{
			name:     "GPT-5.2",
			modelID:  models.GPT52,
			apiModel: "openai/gpt-5.2",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(llm.DriverOptions{
				Model: models.Model{
					ID:       tt.modelID,
					APIModel: tt.apiModel,
				},
			})

			result := client.isGeminiModel()
			assert.Equal(t, tt.expected, result, "isGeminiModel() for %s", tt.name)
		})
	}
}

func TestConvertMessagesForGemini_WithThoughtSignatures(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.Gemini31ProPreview,
			APIModel: "google/gemini-3.1-pro-preview",
		},
	})

	// Create messages with tool calls that have thought signatures
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "What's the weather?"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Let me check the weather for you."},
				message.ToolCall{
					ID:               "call_abc123",
					Name:             "get_weather",
					Input:            `{"location": "Boston"}`,
					ThoughtSignature: "encrypted_thought_data_base64==",
				},
			},
		},
		{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call_abc123",
					Name:       "get_weather",
					Content:    `{"temperature": 72, "condition": "sunny"}`,
				},
			},
		},
	}

	result := client.convertMessagesForGemini([]string{"You are a helpful assistant."}, messages)

	// Should have 4 messages: system + user + assistant + tool
	require.Len(t, result, 4)

	// Check system message
	assert.Equal(t, "system", result[0]["role"])
	assert.Equal(t, "You are a helpful assistant.", result[0]["content"])

	// Check user message
	assert.Equal(t, "user", result[1]["role"])
	assert.Equal(t, "What's the weather?", result[1]["content"])

	// Check assistant message has reasoning_details
	assert.Equal(t, "assistant", result[2]["role"])
	assert.Equal(t, "Let me check the weather for you.", result[2]["content"])

	// Verify tool_calls
	toolCalls, ok := result[2]["tool_calls"].([]map[string]interface{})
	require.True(t, ok, "tool_calls should be a slice of maps")
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_abc123", toolCalls[0]["id"])

	// Verify reasoning_details is present with the thought signature
	reasoningDetails, ok := result[2]["reasoning_details"].([]map[string]interface{})
	require.True(t, ok, "reasoning_details should be present")
	require.Len(t, reasoningDetails, 1)
	assert.Equal(t, "reasoning.encrypted", reasoningDetails[0]["type"])
	assert.Equal(t, "call_abc123", reasoningDetails[0]["id"])
	assert.Equal(t, "encrypted_thought_data_base64==", reasoningDetails[0]["data"])

	// Check tool message
	assert.Equal(t, "tool", result[3]["role"])
	assert.Equal(t, "call_abc123", result[3]["tool_call_id"])
}

func TestConvertMessagesForGemini_WithoutThoughtSignatures(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.Gemini31ProPreview,
			APIModel: "google/gemini-3.1-pro-preview",
		},
	})

	// Create messages with tool calls that don't have thought signatures
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:               "call_xyz789",
					Name:             "some_tool",
					Input:            `{}`,
					ThoughtSignature: "", // No signature
				},
			},
		},
	}

	result := client.convertMessagesForGemini([]string{}, messages)

	// Should have 2 messages: user + assistant
	require.Len(t, result, 2)

	// Check assistant message does NOT have reasoning_details when signature is empty
	_, hasReasoningDetails := result[1]["reasoning_details"]
	assert.False(t, hasReasoningDetails, "reasoning_details should not be present when thought signature is empty")
}

func TestReasoningDetailParsing(t *testing.T) {
	// Test that we can parse reasoning_details from an OpenRouter response
	responseJSON := `{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "google/gemini-3.1-pro-preview",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"reasoning_details": [
					{
						"type": "reasoning.encrypted",
						"id": "call_abc123",
						"data": "encrypted_thought_data_base64==",
						"format": "gemini-v1"
					}
				],
				"tool_calls": [
					{
						"id": "call_abc123",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\": \"Boston\"}"
						}
					}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`

	var resp OpenRouterResponse
	err := json.Unmarshal([]byte(responseJSON), &resp)
	require.NoError(t, err)

	// Verify reasoning_details was parsed
	require.Len(t, resp.Choices, 1)
	require.Len(t, resp.Choices[0].Message.ReasoningDetails, 1)

	rd := resp.Choices[0].Message.ReasoningDetails[0]
	assert.Equal(t, "reasoning.encrypted", rd.Type)
	assert.Equal(t, "call_abc123", rd.ID)
	assert.Equal(t, "encrypted_thought_data_base64==", rd.Data)

	// Verify tool_calls was parsed
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	tc := resp.Choices[0].Message.ToolCalls[0]
	assert.Equal(t, "call_abc123", tc.ID)
	assert.Equal(t, "get_weather", tc.Function.Name)
}
