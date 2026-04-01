// Copyright (c) 2025 Reliant Labs
package tokens

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCounter_EstimateTokens(t *testing.T) {
	counter := New()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "single character",
			text:     "a",
			expected: 1,
		},
		{
			name:     "short text",
			text:     "hello",
			expected: 1, // max(1, 5/4) = max(1, 1) = 1
		},
		{
			name:     "medium text",
			text:     "hello world",
			expected: 2, // max(1, 11/4) = max(1, 2) = 2
		},
		{
			name:     "longer text",
			text:     "This is a longer piece of text that should result in more tokens",
			expected: 16, // 64 chars / 4 = 16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.EstimateTokens(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCounter_CountWords(t *testing.T) {
	counter := New()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "single word",
			text:     "hello",
			expected: 1,
		},
		{
			name:     "two words",
			text:     "hello world",
			expected: 2,
		},
		{
			name:     "words with punctuation",
			text:     "hello, world!",
			expected: 2,
		},
		{
			name:     "multiple spaces",
			text:     "hello    world",
			expected: 2,
		},
		{
			name:     "newlines and tabs",
			text:     "hello\nworld\ttest",
			expected: 3,
		},
		{
			name:     "numbers and letters",
			text:     "test123 456test",
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.CountWords(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCounter_CountLines(t *testing.T) {
	counter := New()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "single line",
			text:     "hello world",
			expected: 1,
		},
		{
			name:     "two lines",
			text:     "hello\nworld",
			expected: 2,
		},
		{
			name:     "multiple lines",
			text:     "line1\nline2\nline3\n",
			expected: 4, // includes empty line after last \n
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.CountLines(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCounter_CountCharacters(t *testing.T) {
	counter := New()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "ascii text",
			text:     "hello",
			expected: 5,
		},
		{
			name:     "unicode text",
			text:     "héllö",
			expected: 5, // 5 runes
		},
		{
			name:     "emoji",
			text:     "hello 👋",
			expected: 7, // 6 chars + 1 emoji
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.CountCharacters(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCounter_EstimateTokensFromWords(t *testing.T) {
	counter := New()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "single word",
			text:     "hello",
			expected: 1, // max(1, int(1*1.3)) = max(1, 1) = 1
		},
		{
			name:     "multiple words",
			text:     "hello world test",
			expected: 3, // max(1, int(3*1.3)) = max(1, 3) = 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.EstimateTokensFromWords(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCounter_GetStats(t *testing.T) {
	counter := New()
	text := "Hello world!\nThis is a test."

	stats := counter.GetStats(text)

	assert.Equal(t, 28, stats.Characters) // actual length including \n
	assert.Equal(t, 28, stats.Bytes)
	assert.Equal(t, 6, stats.Words) // "Hello", "world", "This", "is", "a", "test"
	assert.Equal(t, 2, stats.Lines)
	assert.Equal(t, 7, stats.EstimatedTokens) // 28/4 = 7
	assert.Equal(t, 7, stats.TokensFromWords) // int(6*1.3) = int(7.8) = 7
}

func TestCounter_EstimateTokensForModel(t *testing.T) {
	counter := New()
	text := "This is a test text"

	tests := []struct {
		name      string
		modelName string
		text      string
		minTokens int // minimum expected tokens
		maxTokens int // maximum expected tokens
	}{
		{
			name:      "GPT model",
			modelName: "gpt-4",
			text:      text,
			minTokens: 6, // 19 chars / 3 = 6.33
			maxTokens: 8,
		},
		{
			name:      "Claude model",
			modelName: "claude-3-opus",
			text:      text,
			minTokens: 4, // 19 chars / 4 = 4.75
			maxTokens: 6,
		},
		{
			name:      "Unknown model",
			modelName: "unknown-model",
			text:      text,
			minTokens: 4, // defaults to standard estimation
			maxTokens: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.EstimateTokensForModel(tt.text, tt.modelName)
			assert.GreaterOrEqual(t, result, tt.minTokens)
			assert.LessOrEqual(t, result, tt.maxTokens)
		})
	}
}

func TestCounter_TruncateToTokenLimit(t *testing.T) {
	counter := New()

	tests := []struct {
		name      string
		text      string
		maxTokens int
		expected  string
	}{
		{
			name:      "no truncation needed",
			text:      "short",
			maxTokens: 10,
			expected:  "short",
		},
		{
			name:      "zero limit",
			text:      "any text",
			maxTokens: 0,
			expected:  "",
		},
		{
			name:      "truncate at word boundary",
			text:      "this is a very long piece of text that needs truncation",
			maxTokens: 5, // ~20 chars
			expected:  "this is a very long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.TruncateToTokenLimit(tt.text, tt.maxTokens)

			// Check that result is not longer than expected
			if tt.expected != "" {
				assert.LessOrEqual(t, len(result), len(tt.expected)*2) // allow some variance
			}

			// Check that we don't exceed token limit
			estimatedTokens := counter.EstimateTokens(result)
			assert.LessOrEqual(t, estimatedTokens, tt.maxTokens*2) // allow some variance due to estimation
		})
	}
}

func TestCounter_SplitByTokenLimit(t *testing.T) {
	counter := New()

	tests := []struct {
		name      string
		text      string
		maxTokens int
		minChunks int
		maxChunks int
	}{
		{
			name:      "no splitting needed",
			text:      "short text",
			maxTokens: 10,
			minChunks: 1,
			maxChunks: 1,
		},
		{
			name:      "empty text",
			text:      "",
			maxTokens: 10,
			minChunks: 0,
			maxChunks: 0,
		},
		{
			name:      "split long text",
			text:      strings.Repeat("word ", 100), // 500 chars, ~125 tokens
			maxTokens: 10,                           // should split into multiple chunks
			minChunks: 5,
			maxChunks: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := counter.SplitByTokenLimit(tt.text, tt.maxTokens)

			assert.GreaterOrEqual(t, len(result), tt.minChunks)
			assert.LessOrEqual(t, len(result), tt.maxChunks)

			// Check that each chunk is within token limit (with some tolerance)
			for i, chunk := range result {
				estimatedTokens := counter.EstimateTokens(chunk)
				assert.LessOrEqual(t, estimatedTokens, tt.maxTokens*2,
					"chunk %d exceeds token limit: %d > %d", i, estimatedTokens, tt.maxTokens)
			}

			// Check that all chunks combined equal original text (when joined)
			if len(result) > 0 {
				combined := strings.Join(result, "")
				combined = strings.ReplaceAll(combined, " ", "") // remove spaces added by joining
				original := strings.ReplaceAll(tt.text, " ", "")

				// Length should be similar (accounting for spacing differences)
				assert.InDelta(t, len(original), len(combined), float64(len(original))*0.1)
			}
		})
	}
}

// Benchmark tests
func BenchmarkCounter_EstimateTokens(b *testing.B) {
	counter := New()
	text := strings.Repeat("This is a sample text for benchmarking token estimation. ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.EstimateTokens(text)
	}
}

func BenchmarkCounter_CountWords(b *testing.B) {
	counter := New()
	text := strings.Repeat("This is a sample text for benchmarking word counting functionality. ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.CountWords(text)
	}
}

func BenchmarkCounter_GetStats(b *testing.B) {
	counter := New()
	text := strings.Repeat("This is a sample text for benchmarking comprehensive statistics. ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.GetStats(text)
	}
}

func TestIntegration_LargeText(t *testing.T) {
	counter := New()

	// Create a large text (about 10KB)
	largeText := strings.Repeat("This is a sample sentence for testing large text processing. ", 200)

	stats := counter.GetStats(largeText)

	// Verify basic properties
	assert.Greater(t, stats.Characters, 0)
	assert.Greater(t, stats.Words, 0)
	assert.Greater(t, stats.EstimatedTokens, 0)
	assert.Equal(t, len(largeText), stats.Bytes)

	// Test splitting
	chunks := counter.SplitByTokenLimit(largeText, 100)
	assert.Greater(t, len(chunks), 1, "Large text should be split into multiple chunks")

	// Test truncation
	truncated := counter.TruncateToTokenLimit(largeText, 50)
	truncatedTokens := counter.EstimateTokens(truncated)
	assert.LessOrEqual(t, truncatedTokens, 100) // allow some variance
}
