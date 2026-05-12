// Copyright (c) 2025 Reliant Labs
package recovery

import (
	"testing"
)

// TestRecoveryWithProductionDatabase tests recovery using the actual broken chat from production.
// This test originally read a SQLite production database at ./data/reliant.db.
// It requires a separate SQLite driver and read-only repository wrapper to function.
//
// Broken chat ID: 1102724f-fe26-4efa-a934-63cd25301760
func TestRecoveryWithProductionDatabase(t *testing.T) {
	t.Skip("TODO: convert from SQLite to Postgres — this test reads a SQLite production database file")
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
