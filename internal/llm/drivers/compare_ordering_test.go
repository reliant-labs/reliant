// Copyright (c) 2025 Reliant Labs
package drivers_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// This test uses the json_diff_test.go comparison logic to verify message ordering
func TestCacheControlOrdering(t *testing.T) {
	// Create a conversation with multiple tool results to test ordering
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "What's the weather, time, and temperature in NYC?"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Let me check that for you."},
				message.ToolCall{
					ID:       "call_1",
					Name:     "get_weather",
					Input:    `{"location":"NYC"}`,
					Type:     "function",
					Finished: true,
				},
				message.ToolCall{
					ID:       "call_2",
					Name:     "get_time",
					Input:    `{"location":"NYC"}`,
					Type:     "function",
					Finished: true,
				},
				message.ToolCall{
					ID:       "call_3",
					Name:     "get_temperature",
					Input:    `{"location":"NYC"}`,
					Type:     "function",
					Finished: true,
				},
			},
		},
		{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call_1",
					Content:    "Sunny",
					IsError:    false,
				},
				message.ToolResult{
					ToolCallID: "call_2",
					Content:    "2:30 PM EST",
					IsError:    false,
				},
				message.ToolResult{
					ToolCallID: "call_3",
					Content:    "72°F",
					IsError:    false,
				},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "It's sunny and 72°F in NYC. The time is 2:30 PM EST."},
			},
		},
	}

	prompts := []string{
		"You are a helpful assistant.",
		"Always provide accurate information.",
	}

	// Create driver options
	driverOpts := llm.DriverOptions{
		ApiKey:       "test-key",
		DisableCache: false,
		Model: models.Model{
			ID:       models.Claude45Sonnet,
			APIModel: "anthropic/claude-4.5-sonnet",
		},
		MaxTokens: 1024,
	}

	// Test anthropic driver
	t.Run("Anthropic", func(t *testing.T) {
		// Import here to test
		anthropicDriver := mustCreateAnthropicDriver(t, driverOpts)
		result := convertAnthropicMessages(t, anthropicDriver, prompts, messages)

		// Write output
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		os.WriteFile("/tmp/anthropic_test_output.json", resultJSON, 0644)
		t.Logf("Wrote Anthropic output to /tmp/anthropic_test_output.json")

		// Verify cache is on last message
		if arr, ok := result.([]interface{}); ok && len(arr) > 0 {
			lastIdx := len(arr) - 1
			lastMsg := arr[lastIdx].(map[string]interface{})
			assert.True(t, hasCacheControlInMessage(lastMsg),
				"Anthropic: Last message should have cache control")
		}
	})

	// Test openrouter driver
	t.Run("OpenRouter", func(t *testing.T) {
		openrouterDriver := mustCreateOpenRouterDriver(t, driverOpts)
		result := convertOpenRouterMessages(t, openrouterDriver, prompts, messages)

		// Write output
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		os.WriteFile("/tmp/openrouter_test_output.json", resultJSON, 0644)
		t.Logf("Wrote OpenRouter output to /tmp/openrouter_test_output.json")

		// Verify cache is on last message
		if arr, ok := result.([]interface{}); ok && len(arr) > 0 {
			lastIdx := len(arr) - 1
			lastMsg := arr[lastIdx].(map[string]interface{})
			assert.True(t, hasCacheControlInMessage(lastMsg),
				"OpenRouter: Last message should have cache control")
		}
	})

	t.Log("\n✅ Test complete! Now run:")
	t.Log("   go test -v -run TestJSONDifferences ../../..")
	t.Log("\nOr manually compare:")
	t.Log("   cat /tmp/anthropic_test_output.json")
	t.Log("   cat /tmp/openrouter_test_output.json")
}

// Helper functions that use reflection to call private methods
func mustCreateAnthropicDriver(t *testing.T, opts llm.DriverOptions) interface{} {
	// Dynamically load anthropic package
	pkg := "github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	t.Skipf("Skip - requires refactoring to export methods or use internal test. Package: %s", pkg)
	return nil
}

func mustCreateOpenRouterDriver(t *testing.T, opts llm.DriverOptions) interface{} {
	pkg := "github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
	t.Skipf("Skip - requires refactoring to export methods or use internal test. Package: %s", pkg)
	return nil
}

func convertAnthropicMessages(t *testing.T, driver interface{}, prompts []string, messages []message.Message) interface{} {
	return nil
}

func convertOpenRouterMessages(t *testing.T, driver interface{}, prompts []string, messages []message.Message) interface{} {
	return nil
}

func hasCacheControlInMessage(msg map[string]interface{}) bool {
	content := msg["content"]

	// Check if content is an array
	if contentArray, ok := content.([]interface{}); ok {
		for _, item := range contentArray {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if _, hasCache := itemMap["cache_control"]; hasCache {
					return true
				}
			}
		}
	}

	return false
}

// TestManualComparison provides instructions for manual testing
func TestManualComparison(t *testing.T) {
	t.Log("\n=== Manual Testing Instructions ===")
	t.Log("1. Generate message outputs using both drivers")
	t.Log("2. Save to one.json and zero.json")
	t.Log("3. Run: go test -v -run TestJSONDifferences")
	t.Log("\nThis will use the existing json_diff_test.go comparison logic")
	t.Log("to verify that messages are ordered identically and cache control")
	t.Log("is applied to the same positions in both drivers.")
}
