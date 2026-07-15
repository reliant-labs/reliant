package threads

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// =============================================================================
// Tests
// =============================================================================

func TestSaveMessage_Validation(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create a thread for testing
	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("requires chat_id", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "hello",
		})
		if err == nil {
			t.Error("expected error for missing chat_id")
		}
	})

	t.Run("requires thread", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "hello",
		})
		if err == nil {
			t.Error("expected error for missing thread")
		}
	})

	t.Run("validates role", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    99,
			Content: "hello",
		})
		if err == nil {
			t.Error("expected error for invalid role")
		}
	})

	t.Run("validates display_style", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:       h.chatID,
			Thread:       thread.ID,
			Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:      "hello",
			DisplayStyle: 99,
		})
		if err == nil {
			t.Error("expected error for invalid display_style")
		}
	})

	t.Run("user message requires content or attachments", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
		})
		if err == nil {
			t.Error("expected error for user message without content")
		}
	})

	t.Run("tool message requires tool_results", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_TOOL),
		})
		if err == nil {
			t.Error("expected error for tool message without results")
		}
	})
}

func TestSaveMessage_UserMessage(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("creates user message with text", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "Hello, world!",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.MessageID == "" {
			t.Error("expected message ID")
		}
		if result.Ordinal != 0 {
			t.Errorf("expected ordinal 0, got %d", result.Ordinal)
		}
		if result.WasExisting {
			t.Error("expected new message, not existing")
		}

		// Check content block was created via DB query
		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(blocks))
		}
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT {
			t.Errorf("expected text block, got %d", blocks[0].BlockType)
		}
		if *blocks[0].Content != "Hello, world!" {
			t.Errorf("unexpected content: %s", *blocks[0].Content)
		}
	})

	t.Run("creates user message with attachments", func(t *testing.T) {
		att := h.createAttachment("att-1", "test.png", "image")

		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:      h.chatID,
			Thread:      thread.ID,
			Role:        int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Attachments: []string{att.ID},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(blocks))
		}
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE {
			t.Errorf("expected image block, got %d", blocks[0].BlockType)
		}
	})

	t.Run("creates user message with text and attachments", func(t *testing.T) {
		att := h.createAttachment("att-2", "test2.png", "image")

		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:      h.chatID,
			Thread:      thread.ID,
			Role:        int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:     "Check this image",
			Attachments: []string{att.ID},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(blocks))
		}
		// First should be text
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT {
			t.Errorf("expected text block first, got %d", blocks[0].BlockType)
		}
		// Second should be image
		if blocks[1].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE {
			t.Errorf("expected image block second, got %d", blocks[1].BlockType)
		}
	})
}

func TestSaveMessage_AssistantMessage(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("creates assistant message with text", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: "I can help with that!",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(blocks))
		}
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT {
			t.Errorf("expected text block, got %d", blocks[0].BlockType)
		}
	})

	t.Run("creates assistant message with thinking", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: "The answer is 42",
			Thinking: &ThinkingContent{
				Content:   "Let me think about this...",
				Signature: "sig-123",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(blocks))
		}
		// First should be thinking
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING {
			t.Errorf("expected thinking block first, got %d", blocks[0].BlockType)
		}
		if *blocks[0].Content != "Let me think about this..." {
			t.Errorf("unexpected thinking content")
		}
		if *blocks[0].ThoughtSignature != "sig-123" {
			t.Errorf("expected signature to be set")
		}
		// Second should be text
		if blocks[1].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT {
			t.Errorf("expected text block second, got %d", blocks[1].BlockType)
		}
	})

	t.Run("creates assistant message with tool calls", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			ToolCalls: []ToolCall{
				{ID: "tc-1", Name: "read_file", Input: `{"path": "/foo.txt"}`},
				{ID: "tc-2", Name: "write_file", Input: `{"path": "/bar.txt"}`, ThoughtSignature: "thought-sig"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(blocks))
		}
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			t.Errorf("expected tool_call block, got %d", blocks[0].BlockType)
		}
		if *blocks[0].ToolName != "read_file" {
			t.Errorf("expected tool name read_file")
		}
		if *blocks[0].ToolCallID != "tc-1" {
			t.Errorf("expected tool call ID tc-1")
		}
		// Second tool call should have thought signature
		if blocks[1].ThoughtSignature == nil || *blocks[1].ThoughtSignature != "thought-sig" {
			t.Error("expected thought signature on second tool call")
		}

		// Verify pass-through
		if len(result.ToolCalls) != 2 {
			t.Errorf("expected tool calls to be passed through")
		}
	})

	t.Run("allows empty assistant message", func(t *testing.T) {
		// Should not error, just warn
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSaveMessage_ToolMessage(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("creates tool message with results", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_TOOL),
			ToolResults: []ToolResult{
				{ToolCallID: "tc-1", Name: "read_file", Content: "file contents here"},
				{ToolCallID: "tc-2", Name: "write_file", Content: "success", IsError: false},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(blocks))
		}
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
			t.Errorf("expected tool_result block, got %d", blocks[0].BlockType)
		}
		if *blocks[0].ToolCallID != "tc-1" {
			t.Errorf("expected tool call ID tc-1")
		}
		if *blocks[0].Content != "file contents here" {
			t.Errorf("unexpected content")
		}

		// Verify pass-through
		if len(result.ToolResults) != 2 {
			t.Errorf("expected tool results to be passed through")
		}
	})

	t.Run("creates tool message with error result", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_TOOL),
			ToolResults: []ToolResult{
				{ToolCallID: "tc-err", Name: "shell", Content: "command failed", IsError: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(blocks))
		}
		if !*blocks[0].IsError {
			t.Error("expected is_error to be true")
		}
	})
}

func TestSaveMessage_SystemMessage(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("creates system message", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
			Content: "System prompt here",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		msg, err := h.repo.GetMessage(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to get message: %v", err)
		}
		if msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			t.Errorf("expected system role, got %d", msg.Role)
		}
	})
}

func TestSaveMessage_Idempotency(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("returns existing message on duplicate activity_id", func(t *testing.T) {
		activityID := "activity-1"

		// First save
		result1, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:        h.chatID,
			Thread:        thread.ID,
			Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:       "hello",
			ActivityID:    &activityID,
			AttemptNumber: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result1.WasExisting {
			t.Error("first save should not be existing")
		}

		// Second save with same activity ID, attempt 1
		result2, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:        h.chatID,
			Thread:        thread.ID,
			Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:       "hello",
			ActivityID:    &activityID,
			AttemptNumber: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result2.WasExisting {
			t.Error("second save should return existing")
		}
		if result2.MessageID != result1.MessageID {
			t.Error("should return same message ID")
		}
	})

	t.Run("deletes and recreates on retry", func(t *testing.T) {
		retryActivityID := "retry-activity"

		// First save
		result1, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:        h.chatID,
			Thread:        thread.ID,
			Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:       "original",
			ActivityID:    &retryActivityID,
			AttemptNumber: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Retry (attempt > 1) should delete and recreate
		result2, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:        h.chatID,
			Thread:        thread.ID,
			Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:       "retry content",
			ActivityID:    &retryActivityID,
			AttemptNumber: 2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result2.WasExisting {
			t.Error("retry should create new message")
		}
		if result2.MessageID == result1.MessageID {
			t.Error("retry should have new message ID")
		}
	})
}

func TestSaveMessage_ChatUpdate(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("emits chat_update", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:       h.chatID,
			Thread:       thread.ID,
			Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:      "hello",
			DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_INFO),
			TokenCount:   150,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Query chat updates from the database
		updates, err := h.repo.GetUpdatesSince(ctx, h.chatID, 0, 10)
		if err != nil {
			t.Fatalf("failed to get chat updates: %v", err)
		}

		// Find the message update
		var foundUpdate *db.ChatUpdate
		for i := range updates {
			if updates[i].UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE && updates[i].EntityID == result.MessageID {
				foundUpdate = &updates[i]
				break
			}
		}

		if foundUpdate == nil {
			t.Fatal("expected to find chat update for message")
		}

		// Parse the data
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(foundUpdate.Data), &data); err != nil {
			t.Fatalf("failed to parse update data: %v", err)
		}

		if data["role"] != float64(reliantv1.MessageRole_MESSAGE_ROLE_USER) {
			t.Errorf("unexpected role in update")
		}
		if data["display_style"] != float64(reliantv1.DisplayStyle_DISPLAY_STYLE_INFO) {
			t.Errorf("expected display_style in update")
		}
		if data["thread_token_count"].(float64) != 150 {
			t.Errorf("expected thread_token_count: %v", data["thread_token_count"])
		}
	})

	t.Run("message update uses canonical content block input field", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID: h.chatID,
			Thread: thread.ID,
			Role:   int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			ToolCalls: []ToolCall{
				{ID: "tc-canonical", Name: "edit", Input: `{"edits":[{"file_path":"a.txt","old_string":"x","new_string":"y"}]}`},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		updates, err := h.repo.GetUpdatesSince(ctx, h.chatID, 0, 50)
		if err != nil {
			t.Fatalf("failed to get chat updates: %v", err)
		}

		var foundUpdate *db.ChatUpdate
		for i := range updates {
			if updates[i].UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE && updates[i].EntityID == result.MessageID {
				foundUpdate = &updates[i]
				break
			}
		}
		if foundUpdate == nil {
			t.Fatal("expected to find chat update for assistant message")
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(foundUpdate.Data), &payload); err != nil {
			t.Fatalf("failed to parse update data: %v", err)
		}

		blocksAny, ok := payload["content_blocks"].([]interface{})
		if !ok || len(blocksAny) == 0 {
			t.Fatalf("expected non-empty content_blocks array, got: %#v", payload["content_blocks"])
		}

		toolCallBlock := blocksAny[0].(map[string]interface{})
		if _, ok := toolCallBlock["input"]; !ok {
			t.Fatalf("expected content_blocks[0].input to be present, block=%#v", toolCallBlock)
		}
		if _, hasLegacy := toolCallBlock["tool_input"]; hasLegacy {
			t.Fatalf("did not expect legacy content_blocks[0].tool_input, block=%#v", toolCallBlock)
		}
	})
}

func TestSaveMessage_TokenTracking(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("calculates thread token count from opts", func(t *testing.T) {
		thread, _ := h.createThread("token-thread-1", h.chatID)
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:     h.chatID,
			Thread:     thread.ID,
			Role:       int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content:    "response",
			TokenCount: 180,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.ThreadTokenCount != 180 {
			t.Errorf("expected thread token count 180, got %d", result.ThreadTokenCount)
		}
	})

	t.Run("returns zero when no tokens provided", func(t *testing.T) {
		thread, _ := h.createThread("token-thread-2", h.chatID)
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "hello",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// No tokens provided, should be 0
		if result.ThreadTokenCount != 0 {
			t.Errorf("expected thread token count 0, got %d", result.ThreadTokenCount)
		}
	})
}

func TestSaveMessage_FileReference(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)
	att := h.createAttachment("file-1", "doc.txt", "file_reference")

	t.Run("creates file_reference block for file attachments", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:      h.chatID,
			Thread:      thread.ID,
			Role:        int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Attachments: []string{att.ID},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocks, err := h.repo.ListContentBlocks(ctx, result.MessageID)
		if err != nil {
			t.Fatalf("failed to list content blocks: %v", err)
		}
		if len(blocks) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(blocks))
		}
		if blocks[0].BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE {
			t.Errorf("expected file_reference block, got %d", blocks[0].BlockType)
		}
	})

	t.Run("returns error when attachment metadata is missing", func(t *testing.T) {
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:      h.chatID,
			Thread:      thread.ID,
			Role:        int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Attachments: []string{"missing-attachment-id"},
		})
		if err == nil {
			t.Fatal("expected error for missing attachment metadata")
		}
		if !strings.Contains(err.Error(), "failed to get attachment metadata") {
			t.Fatalf("expected metadata error, got: %v", err)
		}
	})
}

func TestSaveMessage_PersistsCost(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID:  h.chatID,
		Thread:  thread.ID,
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content: "costed message",
		Cost:    0.0789,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg, err := h.repo.GetMessage(ctx, result.MessageID)
	if err != nil {
		t.Fatalf("failed to get saved message: %v", err)
	}
	if msg.Cost == nil {
		t.Fatal("expected message cost to be persisted")
	}
	// cost is stored as Postgres REAL (4-byte float), so the float64 round-trip is
	// not bit-exact — compare with a tolerance sized to REAL's ~7 sig digits.
	if diff := *msg.Cost - 0.0789; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("cost = %f, want %f", *msg.Cost, 0.0789)
	}
}

func TestSaveMessage_MessageCount(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-1", h.chatID)

	t.Run("returns correct message count", func(t *testing.T) {
		// Create 5 existing messages
		for i := 0; i < 5; i++ {
			_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
				ChatID:  h.chatID,
				Thread:  thread.ID,
				Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
				Content: "message",
			})
			if err != nil {
				t.Fatalf("failed to create message: %v", err)
			}
		}

		// Save one more and check count
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "hello",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should be existing count + 1 = 6
		if result.MessageCount != 6 {
			t.Errorf("expected message count 6, got %d", result.MessageCount)
		}
	})
}

// TestSaveMessage_RequiresExistingThread tests that SaveMessage fails when
// the thread doesn't exist (threads must be created explicitly before saving messages).
func TestSaveMessage_RequiresExistingThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("errors when thread does not exist", func(t *testing.T) {
		// Try to save a message to a thread that doesn't exist
		_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  "nonexistent-thread",
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "this should fail",
		})
		if err == nil {
			t.Fatal("expected error when saving to nonexistent thread")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expected error to mention 'does not exist', got: %v", err)
		}
	})

	t.Run("succeeds when thread exists", func(t *testing.T) {
		// Create thread first
		thread, cw := h.createThread("existing-thread-test", h.chatID)

		// Now save a message - should succeed
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "this should succeed",
		})
		if err != nil {
			t.Fatalf("SaveMessage failed: %v", err)
		}

		if result.MessageID == "" {
			t.Fatal("expected message ID")
		}
		if result.ContextWindowID != cw.ID {
			t.Errorf("expected context window ID %s, got %s", cw.ID, result.ContextWindowID)
		}
	})
}