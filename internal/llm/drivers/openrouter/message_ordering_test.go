// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// TestSyntheticToolResultOrdering tests tool result ordering with synthetic data
func TestSyntheticToolResultOrdering(t *testing.T) {
	// Create a conversation with multiple tool results
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "What's the weather and time in NYC?"},
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

	prompts := []string{"You are a helpful assistant."}

	// Create OpenRouter driver
	driverOpts := llm.DriverOptions{
		ApiKey:       "test-key",
		DisableCache: false,
		Model: models.Model{
			ID:       models.Claude45Sonnet,
			APIModel: "anthropic/claude-4.5-sonnet",
		},
		MaxTokens: 1024,
	}

	openrouterClient := NewClient(driverOpts)

	// Convert messages using OpenRouter
	openrouterMessages := openrouterClient.convertMessagesWithCacheControl(prompts, messages)

	// Marshal to JSON
	openrouterJSON, _ := json.MarshalIndent(openrouterMessages, "", "  ")

	// Write for inspection
	os.WriteFile("/tmp/openrouter_synthetic.json", openrouterJSON, 0644)

	t.Logf("✅ Wrote synthetic test output to:")
	t.Logf("   /tmp/openrouter_synthetic.json")

	// Parse and check cache locations
	var openrouterParsed []interface{}
	json.Unmarshal(openrouterJSON, &openrouterParsed)

	t.Logf("\nMessage count: OpenRouter=%d", len(openrouterParsed))

	openrouterCacheLocations := findCacheControlLocations(t, openrouterParsed, "OpenRouter")

	t.Logf("\n📍 Cache locations: %v", openrouterCacheLocations)

	// The cache should be on the LAST message only
	assert.NotEmpty(t, openrouterCacheLocations, "Should have at least one cache marker")

	// Verify last message has cache
	if len(openrouterParsed) > 0 {
		lastIdx := len(openrouterParsed) - 1
		lastMsg := openrouterParsed[lastIdx].(map[string]interface{})
		hasCache := hasCacheControl(lastMsg)

		t.Logf("\nLast message (index %d):", lastIdx)
		t.Logf("  Role: %s", lastMsg["role"])
		t.Logf("  Has cache: %v", hasCache)

		assert.True(t, hasCache, "OpenRouter: Last message should have cache control")

		if hasCache {
			t.Logf("✅ OpenRouter: Cache control correctly applied to last message")
		} else {
			t.Errorf("❌ OpenRouter: Cache control NOT on last message!")
			// Show where cache is
			t.Logf("Cache is at indices: %v", openrouterCacheLocations)
		}
	}

	// Verify cache is on the last message (and possibly system prompts/tools)
	// The important thing is that the LAST message has cache control for ordering
	assert.Contains(t, openrouterCacheLocations, len(openrouterParsed)-1,
		"Last message must have cache control for proper ordering")
}

// Helper functions

func findCacheControlLocations(t *testing.T, messages []interface{}, driverName string) []int {
	var locations []int
	for i, msg := range messages {
		msgMap := msg.(map[string]interface{})
		if hasCacheControl(msgMap) {
			role := msgMap["role"]
			locations = append(locations, i)
			t.Logf("   %s: Message %d (role=%s) has cache control", driverName, i, role)
		}
	}
	return locations
}

func hasCacheControl(msg map[string]interface{}) bool {
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
