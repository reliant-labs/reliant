-- +goose Up
-- Migration: Add forked_from_chat_id to workflows table
-- This enables cross-chat fork resolution for chat branching.
--
-- When a chat is branched:
-- - forked_from_chat_id: The parent chat ID (where parent messages live)
-- - forked_from_thread: The parent thread path (usually parent's workflow_id)
-- - forked_at_ordinal: Up to which message ordinal in parent to inherit
--
-- This unifies chat branching with thread forking under a single resolution path
-- in GetMessagesForLLMContext.

-- Add forked_from_chat_id column for cross-chat fork resolution
ALTER TABLE workflows ADD COLUMN forked_from_chat_id TEXT;

-- +goose Down
-- Note: SQLite doesn't support DROP COLUMN, so we'd need to recreate the table
-- For simplicity, we'll leave the column in place during rollback
