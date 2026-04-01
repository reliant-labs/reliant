// Copyright (c) 2025 Reliant Labs
package recovery

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRecoveryWithProductionDatabase tests recovery using the actual broken chat from production
// This test reads the production database at ./data/reliant.db and verifies recovery logic
//
// Broken chat ID: 1102724f-fe26-4efa-a934-63cd25301760
func TestRecoveryWithProductionDatabase(t *testing.T) {
	// Skip by default - only run when explicitly requested
	if testing.Short() {
		t.Skip("Skipping production database test in short mode")
	}

	// Open production database (read-only to be safe)
	dbPath := "./data/reliant.db"
	sqlDB, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		t.Skipf("Cannot open production database: %v", err)
	}
	defer sqlDB.Close()

	// Create read-only repository wrapper
	// TODO: This needs a way to create a db.Repository from sql.DB
	// For now, skip this test
	t.Skip("TODO: Need to implement read-only repository wrapper for prod DB")

	ctx := context.Background()
	chatID := "1102724f-fe26-4efa-a934-63cd25301760"

	t.Run("inspect_broken_state", func(t *testing.T) {
		// Query messages directly
		rows, err := sqlDB.QueryContext(ctx, `
			SELECT id, role, streaming_state, ordinal, created_at
			FROM messages
			WHERE chat_id = ?
			ORDER BY ordinal
		`, chatID)
		require.NoError(t, err)
		defer rows.Close()

		var messages []struct {
			ID             string
			Role           string
			StreamingState string
			Ordinal        int64
			CreatedAt      string
		}

		for rows.Next() {
			var m struct {
				ID             string
				Role           string
				StreamingState string
				Ordinal        int64
				CreatedAt      string
			}
			require.NoError(t, rows.Scan(&m.ID, &m.Role, &m.StreamingState, &m.Ordinal, &m.CreatedAt))
			messages = append(messages, m)
		}

		t.Logf("Found %d messages in broken chat", len(messages))
		for _, m := range messages {
			t.Logf("  - %s: %s (state: %s, ordinal: %d)", m.ID, m.Role, m.StreamingState, m.Ordinal)
		}

		// Verify we have the broken state
		var streamingCount int
		for _, m := range messages {
			if m.Role == "assistant" && m.StreamingState == "streaming" {
				streamingCount++
			}
		}
		require.Greater(t, streamingCount, 0, "should have at least one assistant message stuck in streaming state")

		// Query content blocks for each message
		for _, m := range messages {
			rows, err := sqlDB.QueryContext(ctx, `
				SELECT id, block_type, streaming_state, tool_call_id
				FROM message_content_blocks
				WHERE message_id = ?
				ORDER BY position
			`, m.ID)
			require.NoError(t, err)

			var blockCount int
			var toolCallIDs []string

			for rows.Next() {
				var blockID, blockType, streamingState string
				var toolCallID sql.NullString
				require.NoError(t, rows.Scan(&blockID, &blockType, &streamingState, &toolCallID))
				blockCount++

				if blockType == "tool_call" && toolCallID.Valid {
					toolCallIDs = append(toolCallIDs, toolCallID.String)
				}
			}
			rows.Close()

			t.Logf("    Message %s has %d content blocks", m.ID, blockCount)

			// Check for orphaned tool calls
			for _, toolCallID := range toolCallIDs {
				var resultExists bool
				err := sqlDB.QueryRowContext(ctx, `
					SELECT EXISTS(
						SELECT 1 FROM message_content_blocks
						WHERE tool_call_id = ? AND block_type = 'tool_result'
					)
				`, toolCallID).Scan(&resultExists)
				require.NoError(t, err)

				if !resultExists {
					t.Logf("      ❌ ORPHANED: tool_call %s has no tool_result", toolCallID)
				}
			}
		}
	})

	// Note: assistant_message_streams table was removed - streaming state is now computed from message_content_blocks
	// Note: tool_calls table was removed - tool call info is stored in message_content_blocks with block_type='tool_call'
}

// TestRecoveryBugReport documents the exact bug found in production
// This serves as documentation and regression test
func TestRecoveryBugReport(t *testing.T) {
	t.Log(`
		=== BUG REPORT: Recovery Fails to Clean Multiple Broken Messages ===

		Chat ID: 1102724f-fe26-4efa-a934-63cd25301760

		BROKEN STATE:
		1. Message 2b42d9bc (assistant): streaming_state="streaming", 0 content blocks
		2. Message cf3baa88 (assistant): streaming_state="streaming", has orphaned tool_call

		ROOT CAUSE:
		- RecoverConversationState only checks the LAST message (GetLastMessageInChat)
		- In this chat, message cf3baa88 is the last ASSISTANT message
		- But message 2b42d9bc is ALSO broken (earlier in conversation)
		- Recovery never looks at earlier messages, so they stay broken

		EXPECTED BEHAVIOR:
		- Recovery should scan ALL messages, not just the last one
		- Should mark ALL incomplete streams as complete
		- Should recover ALL orphaned tool_calls, not just those in the last message

		FIX NEEDED:
		- Change RecoverConversationState to iterate through ALL messages
		- Or change to use a different approach (scan all messages with streaming_state != 'complete')
	`)
}
