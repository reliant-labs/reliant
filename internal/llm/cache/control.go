// Copyright (c) 2025 Reliant Labs
package cache

// CacheStrategy defines the optimal caching strategy for Anthropic models
// Maximum of 4 cache breakpoints allowed:
// - 2 for system prompts (for consistency)
// - 1 for the last message
// - 1 for the last tool (if tools are present)
type CacheStrategy struct {
	MaxCacheBlocks     int
	SystemPromptBlocks int // How many system prompts to cache (max 2)
	LastMessageBlocks  int // How many trailing messages to cache
	LastToolBlocks     int // Whether to cache the last tool
}

// DefaultStrategy returns the default caching strategy
func DefaultStrategy() CacheStrategy {
	return CacheStrategy{
		MaxCacheBlocks:     4,
		SystemPromptBlocks: 2,
		LastMessageBlocks:  1,
		LastToolBlocks:     1,
	}
}

// CalculateCacheAllocation determines how to allocate cache blocks optimally
func CalculateCacheAllocation(systemPromptCount, messageCount, toolCount int) (systemBlocks, messageBlocks, toolBlocks int) {
	strategy := DefaultStrategy()

	// Start with desired allocation
	systemBlocks = min(systemPromptCount, strategy.SystemPromptBlocks)
	remainingBlocks := strategy.MaxCacheBlocks - systemBlocks

	// Allocate for tools if present
	if toolCount > 0 && remainingBlocks > 0 {
		toolBlocks = min(1, remainingBlocks) // Only cache last tool
		remainingBlocks -= toolBlocks
	}

	// Use remaining blocks for messages (working backwards from the end)
	if messageCount > 0 && remainingBlocks > 0 {
		messageBlocks = min(messageCount, remainingBlocks)
	}

	return systemBlocks, messageBlocks, toolBlocks
}

// ShouldCacheSystemPrompt returns true if the system prompt at index i should be cached
// Caches the LAST N system prompts to match Anthropic driver behavior
func ShouldCacheSystemPrompt(index int, totalSystemPrompts int, disabled bool) bool {
	if disabled {
		return false
	}
	systemBlocks, _, _ := CalculateCacheAllocation(totalSystemPrompts, 0, 0)
	// Cache the last N system prompts (e.g., if systemBlocks=2 and totalSystemPrompts=3, cache indices 1 and 2)
	return index >= (totalSystemPrompts - systemBlocks)
}

// ShouldCacheMessage returns true if the message at index i should be cached
func ShouldCacheMessage(index int, totalMessages int, totalSystemPrompts int, totalTools int, disabled bool) bool {
	if disabled {
		return false
	}
	_, messageBlocks, _ := CalculateCacheAllocation(totalSystemPrompts, totalMessages, totalTools)
	// Cache the last N messages
	return index >= (totalMessages - messageBlocks)
}

// ShouldCacheTool returns true if the tool at index i should be cached
func ShouldCacheTool(index int, totalTools int, totalSystemPrompts int, totalMessages int, disabled bool) bool {
	if disabled {
		return false
	}
	_, _, toolBlocks := CalculateCacheAllocation(totalSystemPrompts, totalMessages, totalTools)
	// Only cache the last tool
	return toolBlocks > 0 && index == (totalTools-1)
}
