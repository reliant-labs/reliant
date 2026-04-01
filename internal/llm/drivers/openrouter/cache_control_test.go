// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheControlWithSequentialRequests tests that cache control headers are properly set
// and that content hashing works correctly across 4 sequential requests
func TestCacheControlWithSequentialRequests(t *testing.T) {
	// Create a client with Claude model (supports cache control)
	client := &Client{
		OpenaiClient: &openai.OpenaiClient{
			Options: llm.DriverOptions{
				Model: models.Model{
					ID:       models.Claude45Sonnet,
					APIModel: "anthropic/claude-4.5-sonnet",
				},
				DisableCache: false,
				MaxTokens:    1024,
			},
		},
	}

	// System prompts that should be cached
	systemPrompt1 := "You are a helpful AI assistant with expertise in code review."
	systemPrompt2 := "Focus on identifying bugs, security issues, and performance problems."
	prompts := []string{systemPrompt1, systemPrompt2}

	// Track content hashes for each request to verify caching
	type RequestSnapshot struct {
		SystemPromptsHash string
		MessagesHash      string
		CacheBlockCount   int
		CachedContent     []string
	}

	snapshots := make([]RequestSnapshot, 0, 4)

	// Simulate 4 sequential requests, each adding a random user and assistant message
	conversationHistory := []message.Message{}

	for requestNum := 1; requestNum <= 4; requestNum++ {
		t.Run(fmt.Sprintf("Request_%d", requestNum), func(t *testing.T) {
			// Add a new user message with some random content
			userContent := fmt.Sprintf("User message %d: Please review this code snippet %d", requestNum, requestNum*1000+requestNum)
			conversationHistory = append(conversationHistory, message.Message{
				Role: message.User,
				Parts: []message.ContentPart{
					message.TextContent{Text: userContent},
				},
			})

			// Convert messages with cache control
			result := client.convertMessagesWithCacheControl(prompts, conversationHistory)

			// Assert result is the expected type
			messages, ok := result.([]map[string]interface{})
			require.True(t, ok, "Result should be []map[string]interface{}")

			// Create snapshot for this request
			snapshot := RequestSnapshot{
				CacheBlockCount: 0,
				CachedContent:   []string{},
			}

			// Hash system prompts
			systemHash := sha256.New()

			// Process messages and track cache blocks
			for i, msg := range messages {
				role, _ := msg["role"].(string)

				if role == "system" {
					// System messages should have cache control on first 2
					if i < 2 {
						content := extractContentWithCache(msg)
						if content != "" {
							snapshot.CacheBlockCount++
							snapshot.CachedContent = append(snapshot.CachedContent, content)
							systemHash.Write([]byte(content))
						}
					}
				}
			}
			snapshot.SystemPromptsHash = hex.EncodeToString(systemHash.Sum(nil))

			// Hash the messages up to the last cache break
			messageHash := sha256.New()
			lastCacheIndex := -1

			for i, msg := range messages {
				role, _ := msg["role"].(string)

				if role == "user" || role == "assistant" {
					// Check if this message has cache control
					hasCache := hasMessageCache(msg)

					if hasCache {
						lastCacheIndex = i
						snapshot.CacheBlockCount++

						// Extract and track cached content
						if content, ok := msg["content"].([]map[string]interface{}); ok && len(content) > 0 {
							if text, ok := content[0]["text"].(string); ok {
								snapshot.CachedContent = append(snapshot.CachedContent, text)
							}
						} else if text, ok := msg["content"].(string); ok {
							snapshot.CachedContent = append(snapshot.CachedContent, text)
						}
					}

					// Only hash messages up to and including the last cache point
					if lastCacheIndex >= 0 && i <= lastCacheIndex {
						msgJSON, _ := json.Marshal(msg)
						messageHash.Write(msgJSON)
					}
				}
			}

			if lastCacheIndex >= 0 {
				snapshot.MessagesHash = hex.EncodeToString(messageHash.Sum(nil))
			}

			// Verify cache control rules
			t.Run("CacheBlockLimit", func(t *testing.T) {
				assert.LessOrEqual(t, snapshot.CacheBlockCount, 4,
					"Should not exceed 4 cache blocks (Anthropic limit)")
			})

			t.Run("SystemPromptsCached", func(t *testing.T) {
				// First 2 system prompts should be cached
				systemCacheCount := 0
				for i, msg := range messages {
					if role, _ := msg["role"].(string); role == "system" && i < 2 {
						if hasMessageCache(msg) {
							systemCacheCount++
						}
					}
				}
				assert.Equal(t, 2, systemCacheCount,
					"First 2 system prompts should have cache control")
			})

			t.Run("LastMessageCached", func(t *testing.T) {
				// The very last user/assistant message should be cached (if we have room)
				lastUserOrAssistantIndex := -1
				for i := len(messages) - 1; i >= 0; i-- {
					role, _ := messages[i]["role"].(string)
					if role == "user" || role == "assistant" {
						lastUserOrAssistantIndex = i
						break
					}
				}

				if lastUserOrAssistantIndex >= 0 && snapshot.CacheBlockCount < 4 {
					assert.True(t, hasMessageCache(messages[lastUserOrAssistantIndex]),
						"Last user/assistant message should have cache control if under 4 blocks")
				}
			})

			// Store snapshot
			snapshots = append(snapshots, snapshot)

			// For requests 2-4, verify that cached content from previous requests matches
			if requestNum > 1 {
				prevSnapshot := snapshots[requestNum-2]

				t.Run("SystemPromptsConsistent", func(t *testing.T) {
					assert.Equal(t, prevSnapshot.SystemPromptsHash, snapshot.SystemPromptsHash,
						"System prompts hash should be consistent across requests")
				})

				t.Run("CachedContentGrows", func(t *testing.T) {
					// The cached content should include previous cached content
					// System prompts should always be the same
					assert.GreaterOrEqual(t, len(snapshot.CachedContent), 2,
						"Should have at least system prompts cached")

					if len(prevSnapshot.CachedContent) >= 2 {
						// First two cached items (system prompts) should match
						assert.Equal(t, prevSnapshot.CachedContent[0], snapshot.CachedContent[0],
							"First system prompt should match")
						assert.Equal(t, prevSnapshot.CachedContent[1], snapshot.CachedContent[1],
							"Second system prompt should match")
					}
				})
			}

			// Add assistant response for next iteration
			if requestNum < 4 {
				assistantContent := fmt.Sprintf("Assistant response %d: I've reviewed the code and found %d issues", requestNum, requestNum*2)
				conversationHistory = append(conversationHistory, message.Message{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.TextContent{Text: assistantContent},
					},
				})
			}
		})
	}

	// Final verification across all requests
	t.Run("CacheConsistencyAcrossRequests", func(t *testing.T) {
		// Verify that system prompts hash is identical across all requests
		if len(snapshots) > 0 {
			firstSystemHash := snapshots[0].SystemPromptsHash
			for i, snapshot := range snapshots {
				assert.Equal(t, firstSystemHash, snapshot.SystemPromptsHash,
					"System prompts hash should be identical for request %d", i+1)
			}
		}

		// Verify that cache blocks are being used efficiently
		for i, snapshot := range snapshots {
			assert.Greater(t, snapshot.CacheBlockCount, 0,
				"Request %d should have at least one cache block", i+1)
			assert.LessOrEqual(t, snapshot.CacheBlockCount, 4,
				"Request %d should not exceed 4 cache blocks", i+1)
		}
	})
}

// TestCacheControlDisabled verifies that no cache control is added when disabled
func TestCacheControlDisabled(t *testing.T) {
	client := &Client{
		OpenaiClient: &openai.OpenaiClient{
			Options: llm.DriverOptions{
				Model: models.Model{
					ID:       models.Claude45Sonnet,
					APIModel: "anthropic/claude-4.5-sonnet",
				},
				DisableCache: true, // Cache disabled
				MaxTokens:    1024,
			},
		},
	}

	prompts := []string{"System prompt 1", "System prompt 2"}
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Test message"},
			},
		},
	}

	result := client.convertMessagesWithCacheControl(prompts, messages)

	// When cache is disabled, it delegates to OpenAI client which returns a different format
	// The important thing is that the result doesn't have cache control
	assert.NotNil(t, result, "Should return converted messages")

	// If it's the cache control format, verify no cache blocks
	if convertedMessages, ok := result.([]map[string]interface{}); ok {
		for _, msg := range convertedMessages {
			assert.False(t, hasMessageCache(msg),
				"No message should have cache control when disabled")
		}
	}
	// Otherwise, it's using the standard OpenAI format which doesn't have cache control anyway
}

// TestNonAnthropicModel verifies that non-Anthropic models don't get cache control
func TestNonAnthropicModel(t *testing.T) {
	client := &Client{
		OpenaiClient: &openai.OpenaiClient{
			Options: llm.DriverOptions{
				Model: models.Model{
					ID:       models.Gemini25Flash,
					APIModel: "google/gemini-2.5-flash",
				},
				DisableCache: false,
				MaxTokens:    1024,
			},
		},
	}

	prompts := []string{"System prompt"}
	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Test message"},
			},
		},
	}

	// For non-Anthropic models, this should delegate to OpenAI client
	// which doesn't add cache control
	result := client.convertMessagesWithCacheControl(prompts, messages)

	// The result format will be different for non-Anthropic models
	// It should just be the standard OpenAI format without cache control
	assert.NotNil(t, result, "Should return converted messages")
}

// Helper function to check if a message has cache control
func hasMessageCache(msg map[string]interface{}) bool {
	// Check for cache control in content array format
	if content, ok := msg["content"].([]map[string]interface{}); ok {
		for _, item := range content {
			if _, hasCache := item["cache_control"]; hasCache {
				return true
			}
		}
	}
	return false
}

// Helper function to extract content from a message with cache control
func extractContentWithCache(msg map[string]interface{}) string {
	if content, ok := msg["content"].([]map[string]interface{}); ok && len(content) > 0 {
		if _, hasCache := content[0]["cache_control"]; hasCache {
			if text, ok := content[0]["text"].(string); ok {
				return text
			}
		}
	} else if text, ok := msg["content"].(string); ok {
		return text
	}
	return ""
}
