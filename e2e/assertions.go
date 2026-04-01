// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// MaxWaitTime is the maximum time to wait for e2e async workflow progress.
// Keep this comfortably above normal local run time to avoid flaky CI timeouts.
const MaxWaitTime = 12 * time.Second

// PollInterval is the interval between condition checks
const PollInterval = 50 * time.Millisecond

// ============================================================================
// MESSAGE ASSERTIONS
// ============================================================================

// AssertMessageCountEventually waits for a specific message count
func AssertMessageCountEventually(t *testing.T, repo db.Repository, chatID string, expected int) []*db.Message {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			messages, _ := repo.ListMessages(context.Background(), chatID, db.MessageListOptions{})
			t.Fatalf("timeout waiting for %d messages, got %d (chatID: %s)", expected, len(messages), chatID)
			return nil
		case <-ticker.C:
			messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
			if err != nil {
				continue
			}
			if len(messages) == expected {
				return messages
			}
		}
	}
}

// AssertMessageCountAtLeast waits for at least N messages
func AssertMessageCountAtLeast(t *testing.T, repo db.Repository, chatID string, minCount int) []*db.Message {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			messages, _ := repo.ListMessages(context.Background(), chatID, db.MessageListOptions{})
			t.Fatalf("timeout waiting for at least %d messages, got %d (chatID: %s)", minCount, len(messages), chatID)
			return nil
		case <-ticker.C:
			messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
			if err != nil {
				continue
			}
			if len(messages) >= minCount {
				return messages
			}
		}
	}
}

// AssertMessagesContainRole checks that messages include a specific role
func AssertMessagesContainRole(t *testing.T, messages []*db.Message, role reliantv1.MessageRole) *db.Message {
	t.Helper()

	for _, msg := range messages {
		if msg.Role == role {
			return msg
		}
	}

	roles := make([]reliantv1.MessageRole, len(messages))
	for i, msg := range messages {
		roles[i] = msg.Role
	}
	t.Fatalf("no message found with role %v, have: %v", role, roles)
	return nil
}

// AssertMessageRolesInOrder checks messages have roles in order
func AssertMessageRolesInOrder(t *testing.T, messages []*db.Message, expectedRoles ...reliantv1.MessageRole) {
	t.Helper()

	require.Len(t, messages, len(expectedRoles), "message count mismatch")

	for i, role := range expectedRoles {
		require.Equal(t, role, messages[i].Role,
			fmt.Sprintf("message %d: expected role %d, got %d", i, role, messages[i].Role))
	}
}

// AssertMessageOrdinalsSequential checks ordinals are 0, 1, 2, ...
func AssertMessageOrdinalsSequential(t *testing.T, messages []*db.Message) {
	t.Helper()

	for i, msg := range messages {
		require.Equal(t, int64(i), msg.Ordinal,
			fmt.Sprintf("message %d: expected ordinal %d, got %d", i, i, msg.Ordinal))
	}
}

// ============================================================================
// CONTENT BLOCK ASSERTIONS
// ============================================================================

// AssertContentBlockExists checks that a message has a content block of the given type
func AssertContentBlockExists(t *testing.T, repo db.Repository, messageID string, blockType reliantv1.ContentBlockType) *db.MessageContentBlock {
	t.Helper()

	ctx := context.Background()
	blocks, err := repo.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == blockType {
			return block
		}
	}

	types := make([]reliantv1.ContentBlockType, len(blocks))
	for i, block := range blocks {
		types[i] = block.BlockType
	}
	t.Fatalf("no content block found with type %v, have: %v", blockType, types)
	return nil
}

// AssertTextContentContains checks that a message has text containing the given string
func AssertTextContentContains(t *testing.T, repo db.Repository, messageID string, expected string) {
	t.Helper()

	ctx := context.Background()
	blocks, err := repo.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
			if strings.Contains(*block.Content, expected) {
				return
			}
		}
	}

	t.Fatalf("no text content found containing %q", expected)
}

// AssertToolCallExists checks that a message has a tool call with the given name
func AssertToolCallExists(t *testing.T, repo db.Repository, messageID string, toolName string) *db.MessageContentBlock {
	t.Helper()

	ctx := context.Background()
	blocks, err := repo.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolName != nil && *block.ToolName == toolName {
			return block
		}
	}

	var toolNames []string
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolName != nil {
			toolNames = append(toolNames, *block.ToolName)
		}
	}
	t.Fatalf("no tool call found with name %q, have: %v", toolName, toolNames)
	return nil
}

// AssertToolResultExists checks that a message has a tool result
func AssertToolResultExists(t *testing.T, repo db.Repository, messageID string, toolCallID string) *db.MessageContentBlock {
	t.Helper()

	ctx := context.Background()
	blocks, err := repo.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && block.ToolCallID != nil && *block.ToolCallID == toolCallID {
			return block
		}
	}

	t.Fatalf("no tool result found for tool_call_id %q", toolCallID)
	return nil
}

// AssertNoToolCalls checks that a message has no tool calls
func AssertNoToolCalls(t *testing.T, repo db.Repository, messageID string) {
	t.Helper()

	ctx := context.Background()
	blocks, err := repo.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			name := "<unnamed>"
			if block.ToolName != nil {
				name = *block.ToolName
			}
			t.Fatalf("unexpected tool call found: %s", name)
		}
	}
}

// ============================================================================
// CHAT UPDATE ASSERTIONS
// ============================================================================

// AssertChatUpdateExists checks that a chat update of the given type exists
func AssertChatUpdateExists(t *testing.T, repo db.Repository, chatID string, updateType reliantv1.ChatUpdateType) *db.ChatUpdate {
	t.Helper()

	ctx := context.Background()
	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 100)
	require.NoError(t, err, "failed to list chat updates")

	for _, update := range updates {
		if update.UpdateType == updateType {
			return &update
		}
	}

	types := make([]reliantv1.ChatUpdateType, len(updates))
	for i, update := range updates {
		types[i] = update.UpdateType
	}
	t.Fatalf("no chat update found with type %v, have: %v", updateType, types)
	return nil
}

// AssertChatUpdatesEventually waits for chat updates to exist
func AssertChatUpdatesEventually(t *testing.T, repo db.Repository, chatID string, minCount int) []db.ChatUpdate {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			updates, _ := repo.GetUpdatesSince(context.Background(), chatID, 0, 100)
			t.Fatalf("timeout waiting for at least %d chat updates, got %d", minCount, len(updates))
			return nil
		case <-ticker.C:
			updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 100)
			if err != nil {
				continue
			}
			if len(updates) >= minCount {
				return updates
			}
		}
	}
}

// ============================================================================
// WORKFLOW ASSERTIONS
// ============================================================================

// AssertChatState checks that a chat has the expected state
func AssertChatState(t *testing.T, repo db.Repository, chatID string, expected db.ChatState) {
	t.Helper()

	ctx := context.Background()
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err, "failed to get chat")
	require.Equal(t, expected, chat.State, "unexpected chat state")
}

// AssertChatStateEventually waits for a specific chat state
func AssertChatStateEventually(t *testing.T, repo db.Repository, chatID string, expected db.ChatState) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			chat, _ := repo.GetChat(context.Background(), chatID)
			t.Fatalf("timeout waiting for chat state %q, got %q", expected, chat.State)
		case <-ticker.C:
			chat, err := repo.GetChat(ctx, chatID)
			if err != nil {
				continue
			}
			if chat.State == expected {
				return
			}
		}
	}
}

// ============================================================================
// GENERIC HELPERS
// ============================================================================

// WaitFor waits for a condition to be true (max 3s)
func WaitFor(t *testing.T, condition func() bool, msg string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for condition: %s", msg)
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

// WaitForNot waits for a condition to be false (max 3s)
func WaitForNot(t *testing.T, condition func() bool, msg string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for condition to be false: %s", msg)
		case <-ticker.C:
			if !condition() {
				return
			}
		}
	}
}

// MustComplete runs a function and fails if it takes longer than 3s
func MustComplete(t *testing.T, fn func(), msg string) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(MaxWaitTime):
		t.Fatalf("timeout: %s", msg)
	}
}
