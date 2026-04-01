// Copyright (c) 2025 Reliant Labs

// Package tokens provides utilities for counting and estimating tokens in text.
//
// This package offers various methods for token estimation, including:
// - Character-based estimation (commonly ~4 chars per token)
// - Word-based estimation (commonly ~1.3 tokens per word)
// - Model-specific estimation
// - Text truncation and splitting based on token limits
//
// Example usage:
//
//	counter := tokens.New()
//	tokenCount := counter.EstimateTokens("Hello, world!")
//	stats := counter.GetStats("Some longer text...")
//	chunks := counter.SplitByTokenLimit(longText, 1000)
package tokens

// Package-level convenience functions for common operations

var defaultCounter = New()

// EstimateTokens provides a quick token estimate using the default counter.
// This is a convenience function for simple use cases.
func EstimateTokens(text string) int {
	return defaultCounter.EstimateTokens(text)
}

// CountWords counts words in the text using the default counter.
func CountWords(text string) int {
	return defaultCounter.CountWords(text)
}

// GetStats returns comprehensive text statistics using the default counter.
func GetStats(text string) TokenStats {
	return defaultCounter.GetStats(text)
}

// TruncateToTokenLimit truncates text to fit within token limits using the default counter.
func TruncateToTokenLimit(text string, maxTokens int) string {
	return defaultCounter.TruncateToTokenLimit(text, maxTokens)
}

// SplitByTokenLimit splits text into chunks that fit within token limits using the default counter.
func SplitByTokenLimit(text string, maxTokens int) []string {
	return defaultCounter.SplitByTokenLimit(text, maxTokens)
}
