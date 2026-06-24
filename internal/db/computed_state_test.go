// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
)

func TestComputeStreamingState_NoBlocks(t *testing.T) {
	// Empty message - should be streaming (incomplete)
	blocks := []MessageContentBlock{}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
	assert.Nil(t, state.StartedAt)
	assert.Nil(t, state.CompletedAt)
}

func TestComputeStreamingState_ValidTextBlock(t *testing.T) {
	now := time.Now()
	content := "Hello, world!"

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

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.False(t, state.IsStreaming)
	assert.True(t, state.IsComplete)
	assert.NotNil(t, state.StartedAt)
	assert.NotNil(t, state.CompletedAt)
	assert.Equal(t, now, *state.StartedAt)
	assert.Equal(t, now, *state.CompletedAt)
}

func TestComputeStreamingState_EmptyTextBlock(t *testing.T) {
	now := time.Now()
	emptyContent := ""

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &emptyContent,
			CreatedAt: now,
		},
	}

	state := ComputeStreamingState(blocks)

	// Empty content = invalid = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
}

func TestComputeStreamingState_NilContentTextBlock(t *testing.T) {
	now := time.Now()

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   nil, // Nil content = invalid
			CreatedAt: now,
		},
	}

	state := ComputeStreamingState(blocks)

	// Nil content = invalid = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
}

func TestComputeStreamingState_ValidToolCallWithResult(t *testing.T) {
	now := time.Now()
	later := now.Add(1 * time.Second)

	toolCallID := "call_abc123"
	toolName := "read_file"
	toolInput := `{"path": "/test.txt"}`
	resultContent := "File contents here"

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
		{
			ID:         "block-2",
			MessageID:  "msg-1",
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    &resultContent,
			CreatedAt:  later,
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.False(t, state.IsStreaming)
	assert.True(t, state.IsComplete)
	assert.NotNil(t, state.StartedAt)
	assert.NotNil(t, state.CompletedAt)
	assert.Equal(t, now, *state.StartedAt)
	assert.Equal(t, later, *state.CompletedAt) // Latest block
}

func TestComputeStreamingState_OrphanedToolCall(t *testing.T) {
	now := time.Now()

	toolCallID := "call_abc123"
	toolName := "read_file"
	toolInput := `{"path": "/test.txt"}`

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
		// No matching tool_result - but that's OK!
		// Tool results are stored in separate TOOL role messages, not in the assistant message
	}

	state := ComputeStreamingState(blocks)

	// Tool call block is valid on its own - the message streaming is complete
	// Tool execution status is tracked separately at the conversation level
	assert.Equal(t, "complete", state.State)
	assert.False(t, state.IsStreaming)
	assert.True(t, state.IsComplete)
	assert.NotNil(t, state.StartedAt)
	assert.NotNil(t, state.CompletedAt)
}

func TestComputeStreamingState_MultipleToolCalls(t *testing.T) {
	now := time.Now()

	toolCallID1 := "call_abc123"
	toolCallID2 := "call_def456"
	toolName := "read_file"
	toolInput := `{"path": "/test.txt"}`
	resultContent := "Result for first call"

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID1,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
		{
			ID:         "block-2",
			MessageID:  "msg-1",
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID1,
			Content:    &resultContent,
			CreatedAt:  now.Add(1 * time.Second),
		},
		{
			ID:         "block-3",
			MessageID:  "msg-1",
			Position:   2,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID2,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now.Add(2 * time.Second),
		},
		// Second tool call has no result in THIS message
		// Tool results are stored in separate TOOL role messages
	}

	state := ComputeStreamingState(blocks)

	// All blocks are valid - message is complete
	assert.Equal(t, "complete", state.State)
	assert.False(t, state.IsStreaming)
	assert.True(t, state.IsComplete)
}

func TestComputeStreamingState_MixedValidBlocks(t *testing.T) {
	now := time.Now()

	textContent := "Here is the file:"
	toolCallID := "call_abc123"
	toolName := "read_file"
	toolInput := `{"path": "/test.txt"}`
	resultContent := "File contents"
	moreText := "Analysis complete."

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &textContent,
			CreatedAt: now,
		},
		{
			ID:         "block-2",
			MessageID:  "msg-1",
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now.Add(1 * time.Second),
		},
		{
			ID:         "block-3",
			MessageID:  "msg-1",
			Position:   2,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    &resultContent,
			CreatedAt:  now.Add(2 * time.Second),
		},
		{
			ID:        "block-4",
			MessageID: "msg-1",
			Position:  3,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &moreText,
			CreatedAt: now.Add(3 * time.Second),
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.False(t, state.IsStreaming)
	assert.True(t, state.IsComplete)
	assert.Equal(t, now, *state.StartedAt)                      // Earliest block
	assert.Equal(t, now.Add(3*time.Second), *state.CompletedAt) // Latest block
}

func TestComputeStreamingState_ToolCallMissingToolCallID(t *testing.T) {
	now := time.Now()

	toolName := "read_file"
	toolInput := `{"path": "/test.txt"}`

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: nil, // Invalid - missing tool_call_id
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
	}

	state := ComputeStreamingState(blocks)

	// Invalid tool call = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
}

func TestComputeStreamingState_ToolResultMissingContent(t *testing.T) {
	now := time.Now()

	toolCallID := "call_abc123"
	toolName := "read_file"
	toolInput := `{"path": "/test.txt"}`

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
		{
			ID:         "block-2",
			MessageID:  "msg-1",
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    nil, // Invalid - missing content
			CreatedAt:  now.Add(1 * time.Second),
		},
	}

	state := ComputeStreamingState(blocks)

	// Invalid tool result = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
}

func TestComputeStreamingState_ProductionBrokenCase(t *testing.T) {
	// This reproduces the exact broken state found in production:
	// Chat 1102724f-fe26-4efa-a934-63cd25301760 had assistant messages with 0 content blocks

	blocks := []MessageContentBlock{}

	state := ComputeStreamingState(blocks)

	// 0 blocks = streaming (broken state)
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)

	// This should be detected by recovery and the message should be deleted
}

func TestComputeStreamingState_BranchedChatCase(t *testing.T) {
	// This reproduces the branched chat issue:
	// Chat 5483f9f0-6781-447c-b7f4-eb61f6d0b371 had orphaned tool calls after branching
	// Note: Tool results are now stored in separate TOOL role messages,
	// so a tool_call block without a matching tool_result is valid.

	now := time.Now()
	toolCallID := "call_branched"
	toolName := "bash"
	toolInput := `{"command": "echo test"}`

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
		// No tool_result in this message - that's stored in a separate TOOL message
	}

	state := ComputeStreamingState(blocks)

	// Tool call block is valid - message is complete
	assert.Equal(t, "complete", state.State)
	assert.False(t, state.IsStreaming)
	assert.True(t, state.IsComplete)
}

// ============================================================================
// EDGE CASES
// ============================================================================

func TestComputeStreamingState_LargeNumberOfBlocks(t *testing.T) {
	// Test with 150 blocks to ensure performance and correctness at scale
	now := time.Now()
	blocks := make([]MessageContentBlock, 150)

	for i := 0; i < 150; i++ {
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

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.False(t, state.IsStreaming)
	assert.NotNil(t, state.StartedAt)
	assert.NotNil(t, state.CompletedAt)
	assert.Equal(t, now, *state.StartedAt)                             // First block
	assert.Equal(t, now.Add(149*time.Millisecond), *state.CompletedAt) // Last block
}

func TestComputeStreamingState_BlocksWithSameTimestamps(t *testing.T) {
	// All blocks created at the exact same time
	now := time.Now()
	content1 := "First"
	content2 := "Second"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content1,
			CreatedAt: now,
		},
		{
			ID:        "block-2",
			MessageID: "msg-1",
			Position:  1,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content2,
			CreatedAt: now, // Same timestamp
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.NotNil(t, state.StartedAt)
	assert.NotNil(t, state.CompletedAt)
	// Both should be the same time
	assert.Equal(t, now, *state.StartedAt)
	assert.Equal(t, now, *state.CompletedAt)
}

func TestComputeStreamingState_FutureTimestamps(t *testing.T) {
	// Blocks with future timestamps (shouldn't happen but should be handled)
	future := time.Now().Add(24 * time.Hour)
	content := "Future content"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content,
			CreatedAt: future,
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.Equal(t, future, *state.StartedAt)
	assert.Equal(t, future, *state.CompletedAt)
}

func TestComputeStreamingState_BlocksOutOfOrder(t *testing.T) {
	// Blocks created in non-sequential order (position != chronological)
	now := time.Now()
	content1 := "First"
	content2 := "Second"
	content3 := "Third"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  2, // Position 2 but created first
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content1,
			CreatedAt: now,
		},
		{
			ID:        "block-2",
			MessageID: "msg-1",
			Position:  0, // Position 0 but created second
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content2,
			CreatedAt: now.Add(1 * time.Second),
		},
		{
			ID:        "block-3",
			MessageID: "msg-1",
			Position:  1, // Position 1 but created last
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &content3,
			CreatedAt: now.Add(2 * time.Second),
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	// Should use earliest timestamp, not position
	assert.Equal(t, now, *state.StartedAt)
	assert.Equal(t, now.Add(2*time.Second), *state.CompletedAt)
}

func TestComputeStreamingState_MultipleToolCallsSameTool(t *testing.T) {
	// Multiple calls to the same tool should work fine
	now := time.Now()

	toolCallID1 := "call_1"
	toolCallID2 := "call_2"
	toolName := "bash"
	toolInput1 := `{"command": "echo 1"}`
	toolInput2 := `{"command": "echo 2"}`
	result1 := "1"
	result2 := "2"

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID1,
			ToolName:   &toolName,
			ToolInput:  &toolInput1,
			CreatedAt:  now,
		},
		{
			ID:         "block-2",
			MessageID:  "msg-1",
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID1,
			Content:    &result1,
			CreatedAt:  now.Add(1 * time.Second),
		},
		{
			ID:         "block-3",
			MessageID:  "msg-1",
			Position:   2,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID2,
			ToolName:   &toolName,
			ToolInput:  &toolInput2,
			CreatedAt:  now.Add(2 * time.Second),
		},
		{
			ID:         "block-4",
			MessageID:  "msg-1",
			Position:   3,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID2,
			Content:    &result2,
			CreatedAt:  now.Add(3 * time.Second),
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.False(t, state.IsStreaming)
}

func TestComputeStreamingState_OrphanedToolResult(t *testing.T) {
	// Tool result without matching tool_call (should not cause issues)
	now := time.Now()

	toolCallID := "call_orphaned"
	resultContent := "Orphaned result"

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    &resultContent,
			CreatedAt:  now,
		},
	}

	state := ComputeStreamingState(blocks)

	// Orphaned result is still valid (has tool_call_id and content)
	// There's no tool_call to match, so no orphaned tool_call detected
	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.False(t, state.IsStreaming)
}

func TestComputeStreamingState_MixedValidInvalid(t *testing.T) {
	// Mix of valid and invalid blocks - one invalid makes whole message streaming
	now := time.Now()

	validContent := "Valid text"
	emptyContent := ""
	toolCallID := "call_123"
	toolName := "bash"
	toolInput := `{"command": "test"}`
	resultContent := "Result"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &validContent,
			CreatedAt: now,
		},
		{
			ID:        "block-2",
			MessageID: "msg-1",
			Position:  1,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &emptyContent, // Invalid!
			CreatedAt: now.Add(1 * time.Second),
		},
		{
			ID:         "block-3",
			MessageID:  "msg-1",
			Position:   2,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now.Add(2 * time.Second),
		},
		{
			ID:         "block-4",
			MessageID:  "msg-1",
			Position:   3,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &toolCallID,
			Content:    &resultContent,
			CreatedAt:  now.Add(3 * time.Second),
		},
	}

	state := ComputeStreamingState(blocks)

	// One invalid block = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
	assert.Nil(t, state.CompletedAt)
}

func TestComputeStreamingState_EmptyToolCallID(t *testing.T) {
	// Empty string tool_call_id (not nil, but empty) should be invalid
	now := time.Now()

	emptyToolCallID := ""
	toolName := "bash"
	toolInput := `{"command": "test"}`

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &emptyToolCallID, // Empty string, not nil
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  now,
		},
	}

	state := ComputeStreamingState(blocks)

	// Empty tool_call_id = invalid = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
}

func TestComputeStreamingState_EmptyToolResultID(t *testing.T) {
	// Empty string tool_call_id for tool_result
	now := time.Now()

	emptyToolCallID := ""
	resultContent := "Some result"

	blocks := []MessageContentBlock{
		{
			ID:         "block-1",
			MessageID:  "msg-1",
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &emptyToolCallID, // Empty string
			Content:    &resultContent,
			CreatedAt:  now,
		},
	}

	state := ComputeStreamingState(blocks)

	// Empty tool_call_id = invalid = streaming
	assert.Equal(t, "streaming", state.State)
	assert.True(t, state.IsStreaming)
	assert.False(t, state.IsComplete)
}

func TestComputeStreamingState_VeryLongContent(t *testing.T) {
	// Test with very long content (simulating large file reads)
	now := time.Now()

	// Create a 1MB string (not 10MB to keep test fast)
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

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.False(t, state.IsStreaming)
}

func TestComputeStreamingState_UnicodeContent(t *testing.T) {
	// Test with various unicode characters
	now := time.Now()

	unicodeContent := "Hello 世界 🌍 مرحبا Здравствуй"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &unicodeContent,
			CreatedAt: now,
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.False(t, state.IsStreaming)
}

func TestComputeStreamingState_SpecialCharactersInContent(t *testing.T) {
	// Test with special characters, newlines, tabs, etc.
	now := time.Now()

	specialContent := "Line 1\nLine 2\r\nTab:\tHere\x00Null\x1BEscape"

	blocks := []MessageContentBlock{
		{
			ID:        "block-1",
			MessageID: "msg-1",
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &specialContent,
			CreatedAt: now,
		},
	}

	state := ComputeStreamingState(blocks)

	assert.Equal(t, "complete", state.State)
	assert.True(t, state.IsComplete)
	assert.False(t, state.IsStreaming)
}

// ============================================================================
// PROPERTY-BASED TESTS (Invariants)
// ============================================================================

func TestComputeStreamingState_Property_AllValidBlocksComplete(t *testing.T) {
	// Property: Any message with all valid blocks should be "complete"

	testCases := []struct {
		name   string
		blocks []MessageContentBlock
	}{
		{
			name: "single valid text",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("text"), CreatedAt: time.Now()},
			},
		},
		{
			name: "multiple valid texts",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("a"), CreatedAt: time.Now()},
				{ID: "2", MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("b"), CreatedAt: time.Now()},
			},
		},
		{
			name: "valid tool call with result",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolCallID: ptr.Of("c1"), ToolName: ptr.Of("bash"), ToolInput: ptr.Of("{}"), CreatedAt: time.Now()},
				{ID: "2", MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr.Of("c1"), Content: ptr.Of("result"), CreatedAt: time.Now()},
			},
		},
		{
			name: "complex valid message",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("intro"), CreatedAt: time.Now()},
				{ID: "2", MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolCallID: ptr.Of("c1"), ToolName: ptr.Of("bash"), ToolInput: ptr.Of("{}"), CreatedAt: time.Now()},
				{ID: "3", MessageID: "m", Position: 2, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr.Of("c1"), Content: ptr.Of("r1"), CreatedAt: time.Now()},
				{ID: "4", MessageID: "m", Position: 3, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("outro"), CreatedAt: time.Now()},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			state := ComputeStreamingState(tc.blocks)
			assert.Equal(t, "complete", state.State, "All valid blocks should result in complete state")
			assert.True(t, state.IsComplete)
			assert.False(t, state.IsStreaming)
		})
	}
}

func TestComputeStreamingState_Property_AnyInvalidBlockStreaming(t *testing.T) {
	// Property: Any message with at least one invalid block should be "streaming"
	// Note: Tool calls without results are now considered VALID because
	// tool results are stored in separate TOOL role messages.

	testCases := []struct {
		name   string
		blocks []MessageContentBlock
	}{
		{
			name: "empty text content",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of(""), CreatedAt: time.Now()},
			},
		},
		{
			name: "nil text content",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: nil, CreatedAt: time.Now()},
			},
		},
		{
			name: "tool call with nil tool_call_id",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolCallID: nil, ToolName: ptr.Of("bash"), ToolInput: ptr.Of("{}"), CreatedAt: time.Now()},
			},
		},
		{
			name: "tool result with nil content",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr.Of("c1"), Content: nil, CreatedAt: time.Now()},
			},
		},
		{
			name: "valid + invalid blocks",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("valid"), CreatedAt: time.Now()},
				{ID: "2", MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of(""), CreatedAt: time.Now()}, // Invalid
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			state := ComputeStreamingState(tc.blocks)
			assert.Equal(t, "streaming", state.State, "Any invalid block should result in streaming state")
			assert.True(t, state.IsStreaming)
			assert.False(t, state.IsComplete)
		})
	}
}

func TestComputeStreamingState_Property_StartedBeforeCompleted(t *testing.T) {
	// Property: StartedAt should always be <= CompletedAt (if both exist)

	testCases := []struct {
		name   string
		blocks []MessageContentBlock
	}{
		{
			name: "single block",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("text"), CreatedAt: time.Now()},
			},
		},
		{
			name: "multiple blocks sequential",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("a"), CreatedAt: time.Now()},
				{ID: "2", MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("b"), CreatedAt: time.Now().Add(1 * time.Second)},
			},
		},
		{
			name: "blocks out of position order",
			blocks: []MessageContentBlock{
				{ID: "1", MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("b"), CreatedAt: time.Now().Add(1 * time.Second)},
				{ID: "2", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("a"), CreatedAt: time.Now()},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			state := ComputeStreamingState(tc.blocks)
			if state.StartedAt != nil && state.CompletedAt != nil {
				assert.True(t, state.StartedAt.Before(*state.CompletedAt) || state.StartedAt.Equal(*state.CompletedAt),
					"StartedAt (%v) should be <= CompletedAt (%v)", state.StartedAt, state.CompletedAt)
			}
		})
	}
}

func TestComputeStreamingState_Property_AddingValidBlocksKeepsComplete(t *testing.T) {
	// Property: Adding more valid blocks to a complete message keeps it complete

	now := time.Now()

	// Start with a complete message
	blocks := []MessageContentBlock{
		{ID: "1", MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("initial"), CreatedAt: now},
	}

	state := ComputeStreamingState(blocks)
	assert.Equal(t, "complete", state.State)

	// Add another valid block
	blocks = append(blocks, MessageContentBlock{
		ID:        "2",
		MessageID: "m",
		Position:  1,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of("added"),
		CreatedAt: now.Add(1 * time.Second),
	})

	state = ComputeStreamingState(blocks)
	assert.Equal(t, "complete", state.State, "Adding valid blocks should keep message complete")

	// Add a tool call + result pair
	blocks = append(blocks,
		MessageContentBlock{
			ID:         "3",
			MessageID:  "m",
			Position:   2,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: ptr.Of("c1"),
			ToolName:   ptr.Of("bash"),
			ToolInput:  ptr.Of("{}"),
			CreatedAt:  now.Add(2 * time.Second),
		},
		MessageContentBlock{
			ID:         "4",
			MessageID:  "m",
			Position:   3,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: ptr.Of("c1"),
			Content:    ptr.Of("result"),
			CreatedAt:  now.Add(3 * time.Second),
		},
	)

	state = ComputeStreamingState(blocks)
	assert.Equal(t, "complete", state.State, "Adding valid tool call+result should keep message complete")
}
