// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// ============================================================================
// BENCHMARK TESTS
// ============================================================================

// BenchmarkComputeStreamingState_1Block tests performance with a single block
func BenchmarkComputeStreamingState_1Block(b *testing.B) {
	now := time.Now()
	content := "Test content"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: now,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_10Blocks tests performance with 10 blocks
func BenchmarkComputeStreamingState_10Blocks(b *testing.B) {
	now := time.Now()
	blocks := make([]MessageContentBlock, 10)

	for i := 0; i < 10; i++ {
		content := "Block content"
		blocks[i] = MessageContentBlock{
			ID:        string(rune('a' + i)),
			MessageID: "msg-1",
			Position:  i,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_100Blocks tests performance with 100 blocks
func BenchmarkComputeStreamingState_100Blocks(b *testing.B) {
	now := time.Now()
	blocks := make([]MessageContentBlock, 100)

	for i := 0; i < 100; i++ {
		content := "Block content"
		blocks[i] = MessageContentBlock{
			ID:        string(rune('a' + i)),
			MessageID: "msg-1",
			Position:  i,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_1000Blocks tests performance with 1000 blocks
func BenchmarkComputeStreamingState_1000Blocks(b *testing.B) {
	now := time.Now()
	blocks := make([]MessageContentBlock, 1000)

	for i := 0; i < 1000; i++ {
		content := "Block content"
		blocks[i] = MessageContentBlock{
			ID:        string(rune('a' + (i % 26))),
			MessageID: "msg-1",
			Position:  i,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_ToolCallsAndResults tests performance with mixed tool calls and results
func BenchmarkComputeStreamingState_ToolCallsAndResults(b *testing.B) {
	now := time.Now()
	blocks := make([]MessageContentBlock, 20)

	// Create 10 tool call + result pairs
	for i := 0; i < 10; i++ {
		toolCallID := "call_" + string(rune('a'+i))
		toolName := "bash"
		toolInput := `{"command": "test"}`
		resultContent := "Result"

		blocks[i*2] = MessageContentBlock{
			ID:         "block-" + string(rune('a'+i*2)),
			MessageID:  "msg-1",
			Position:   i * 2,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now.Add(time.Duration(i*2) * time.Millisecond),
		}

		blocks[i*2+1] = MessageContentBlock{
			ID:         "block-" + string(rune('a'+i*2+1)),
			MessageID:  "msg-1",
			Position:   i*2 + 1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    &resultContent,
			CreatedAt:  now.Add(time.Duration(i*2+1) * time.Millisecond),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_OrphanedToolCalls tests performance with orphaned tool calls
func BenchmarkComputeStreamingState_OrphanedToolCalls(b *testing.B) {
	now := time.Now()
	blocks := make([]MessageContentBlock, 10)

	// Create 10 orphaned tool calls
	for i := 0; i < 10; i++ {
		toolCallID := "call_" + string(rune('a'+i))
		toolName := "bash"
		toolInput := `{"command": "test"}`

		blocks[i] = MessageContentBlock{
			ID:         "block-" + string(rune('a'+i)),
			MessageID:  "msg-1",
			Position:   i,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now.Add(time.Duration(i) * time.Millisecond),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_LargeContent tests performance with large content blocks
func BenchmarkComputeStreamingState_LargeContent(b *testing.B) {
	now := time.Now()

	// Create a 1MB string
	longContent := make([]byte, 1024*1024)
	for i := range longContent {
		longContent[i] = 'x'
	}
	longStr := string(longContent)

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &longStr,
			CreatedAt: now,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}

// BenchmarkComputeStreamingState_ComplexMixed tests performance with a realistic mixed scenario
func BenchmarkComputeStreamingState_ComplexMixed(b *testing.B) {
	now := time.Now()
	blocks := make([]MessageContentBlock, 0, 50)

	// Add some text blocks
	for i := 0; i < 5; i++ {
		content := "Text block " + string(rune('a'+i))
		blocks = append(blocks, MessageContentBlock{
			ID:        "text-" + string(rune('a'+i)),
			MessageID: "msg-1",
			Position:  len(blocks),
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: now.Add(time.Duration(len(blocks)) * time.Millisecond),
		})
	}

	// Add 20 tool call + result pairs
	for i := 0; i < 20; i++ {
		toolCallID := "call_" + string(rune('a'+i))
		toolName := "bash"
		toolInput := `{"command": "test"}`
		resultContent := "Result " + string(rune('a'+i))

		blocks = append(blocks, MessageContentBlock{
			ID:         "tool-call-" + string(rune('a'+i)),
			MessageID:  "msg-1",
			Position:   len(blocks),
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now.Add(time.Duration(len(blocks)) * time.Millisecond),
		})

		blocks = append(blocks, MessageContentBlock{
			ID:         "tool-result-" + string(rune('a'+i)),
			MessageID:  "msg-1",
			Position:   len(blocks),
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    &resultContent,
			CreatedAt:  now.Add(time.Duration(len(blocks)) * time.Millisecond),
		})
	}

	// Add more text blocks
	for i := 0; i < 5; i++ {
		content := "Text block end " + string(rune('a'+i))
		blocks = append(blocks, MessageContentBlock{
			ID:        "text-end-" + string(rune('a'+i)),
			MessageID: "msg-1",
			Position:  len(blocks),
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: now.Add(time.Duration(len(blocks)) * time.Millisecond),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeStreamingState(blocks)
	}
}
