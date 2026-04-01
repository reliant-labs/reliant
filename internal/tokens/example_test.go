// Copyright (c) 2025 Reliant Labs
package tokens_test

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/tokens"
)

func ExampleCounter_EstimateTokens() {
	counter := tokens.New()

	text := "Hello, world! This is a sample text."
	tokenCount := counter.EstimateTokens(text)

	fmt.Printf("Text: %q\n", text)
	fmt.Printf("Estimated tokens: %d\n", tokenCount)
	// Output:
	// Text: "Hello, world! This is a sample text."
	// Estimated tokens: 9
}

func ExampleCounter_GetStats() {
	counter := tokens.New()

	text := "Hello world!\nThis is a test with multiple lines."
	stats := counter.GetStats(text)

	fmt.Printf("Characters: %d\n", stats.Characters)
	fmt.Printf("Words: %d\n", stats.Words)
	fmt.Printf("Lines: %d\n", stats.Lines)
	fmt.Printf("Estimated tokens: %d\n", stats.EstimatedTokens)
	// Output:
	// Characters: 48
	// Words: 9
	// Lines: 2
	// Estimated tokens: 12
}

func ExampleTruncateToTokenLimit() {
	longText := "This is a very long piece of text that exceeds our token limit and needs to be truncated."
	truncated := tokens.TruncateToTokenLimit(longText, 10)

	fmt.Printf("Original length: %d characters\n", len(longText))
	fmt.Printf("Truncated length: %d characters\n", len(truncated))
	fmt.Printf("Truncated text: %q\n", truncated)
	// Output:
	// Original length: 89 characters
	// Truncated length: 38 characters
	// Truncated text: "This is a very long piece of text that"
}

func ExampleSplitByTokenLimit() {
	longText := "This is sentence one. This is sentence two. This is sentence three. This is sentence four."
	chunks := tokens.SplitByTokenLimit(longText, 15)

	fmt.Printf("Original text split into %d chunks:\n", len(chunks))
	for i, chunk := range chunks {
		fmt.Printf("Chunk %d: %q\n", i+1, chunk)
	}
	// Output:
	// Original text split into 2 chunks:
	// Chunk 1: "This is sentence one. This is sentence two. This is"
	// Chunk 2: "sentence three. This is sentence four."
}
