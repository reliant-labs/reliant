// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package tokens

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Counter provides various methods for counting tokens in text
type Counter struct {
	// Model-specific configuration could be added here in the future
}

// New creates a new token counter
func New() *Counter {
	return &Counter{}
}

// EstimateTokens provides a rough estimate of tokens from character count
// This uses the common approximation of ~4 characters per token for English text
func (c *Counter) EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return max(1, len(text)/4)
}

// CountWords counts the number of words in the text
// Words are separated by whitespace or punctuation
func (c *Counter) CountWords(text string) int {
	if text == "" {
		return 0
	}

	words := 0
	inWord := false

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inWord {
				words++
				inWord = true
			}
		} else {
			inWord = false
		}
	}

	return words
}

// CountLines counts the number of lines in the text
func (c *Counter) CountLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// CountCharacters counts the number of characters (runes) in the text
func (c *Counter) CountCharacters(text string) int {
	return utf8.RuneCountInString(text)
}

// CountBytes counts the number of bytes in the text
func (c *Counter) CountBytes(text string) int {
	return len(text)
}

// EstimateTokensFromWords estimates tokens based on word count
// Uses the approximation that 1 word ≈ 1.3 tokens on average
func (c *Counter) EstimateTokensFromWords(text string) int {
	words := c.CountWords(text)
	if words == 0 {
		return 0
	}
	return max(1, int(float64(words)*1.3))
}

// TokenStats provides comprehensive statistics about the text
type TokenStats struct {
	Characters      int // Number of Unicode characters (runes)
	Bytes           int // Number of bytes
	Words           int // Number of words
	Lines           int // Number of lines
	EstimatedTokens int // Estimated token count using character-based method
	TokensFromWords int // Estimated token count using word-based method
}

// GetStats returns comprehensive statistics about the text
func (c *Counter) GetStats(text string) TokenStats {
	return TokenStats{
		Characters:      c.CountCharacters(text),
		Bytes:           c.CountBytes(text),
		Words:           c.CountWords(text),
		Lines:           c.CountLines(text),
		EstimatedTokens: c.EstimateTokens(text),
		TokensFromWords: c.EstimateTokensFromWords(text),
	}
}

// EstimateTokensForModel provides model-specific token estimation
// This can be extended in the future to use actual tokenizers for specific models
func (c *Counter) EstimateTokensForModel(text string, modelName string) int {
	if text == "" {
		return 0
	}

	// Basic estimation - can be enhanced with model-specific logic
	switch {
	case strings.Contains(strings.ToLower(modelName), "gpt"):
		// GPT models tend to have slightly more tokens per character
		return max(1, len(text)/3)
	case strings.Contains(strings.ToLower(modelName), "claude"):
		// Claude models - use standard estimation
		return c.EstimateTokens(text)
	case strings.Contains(strings.ToLower(modelName), "gemini"):
		// Gemini models - use standard estimation
		return c.EstimateTokens(text)
	default:
		// Default to standard estimation
		return c.EstimateTokens(text)
	}
}

// TruncateToTokenLimit truncates text to fit within a token limit
// Uses character-based estimation to approximate token count
func (c *Counter) TruncateToTokenLimit(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}

	estimatedTokens := c.EstimateTokens(text)
	if estimatedTokens <= maxTokens {
		return text
	}

	// Calculate approximate character limit
	charLimit := maxTokens * 4 // 4 chars per token average

	if len(text) <= charLimit {
		return text
	}

	// Truncate at character boundary, trying to preserve word boundaries
	truncated := text[:charLimit]

	// Try to truncate at last word boundary to avoid cutting words
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > charLimit/2 {
		truncated = truncated[:lastSpace]
	}

	return truncated
}

// SplitByTokenLimit splits text into chunks that fit within token limits
func (c *Counter) SplitByTokenLimit(text string, maxTokens int) []string {
	if maxTokens <= 0 || text == "" {
		return []string{}
	}

	estimatedTokens := c.EstimateTokens(text)
	if estimatedTokens <= maxTokens {
		return []string{text}
	}

	var chunks []string
	charLimit := maxTokens * 4 // 4 chars per token average

	for len(text) > 0 {
		if len(text) <= charLimit {
			chunks = append(chunks, text)
			break
		}

		// Find a good break point (prefer line breaks, then word boundaries)
		breakPoint := charLimit
		chunk := text[:breakPoint]

		// Try to break at last newline
		if lastNewline := strings.LastIndex(chunk, "\n"); lastNewline > charLimit/2 {
			breakPoint = lastNewline + 1
		} else if lastSpace := strings.LastIndex(chunk, " "); lastSpace > charLimit/2 {
			// Otherwise break at last space
			breakPoint = lastSpace + 1
		}

		chunks = append(chunks, strings.TrimSpace(text[:breakPoint]))
		text = text[breakPoint:]
	}

	return chunks
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
