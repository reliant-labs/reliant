-- +goose Up
-- Migration: Add last_message_at column to chats table
-- This tracks when the last actual message was sent/received, separate from updated_at
-- which can change on state changes (dismiss, markUnread) that shouldn't affect activity sorting

-- Add the new column
ALTER TABLE chats ADD COLUMN last_message_at TIMESTAMP;

-- Initialize last_message_at for existing chats from their most recent message
-- or fall back to created_at if no messages exist
UPDATE chats SET last_message_at = COALESCE(
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = chats.id),
    chats.created_at
);

-- Add index for efficient sorting by last_message_at
CREATE INDEX IF NOT EXISTS idx_chats_last_message_at ON chats(last_message_at DESC);

-- +goose Down
-- Remove the last_message_at column

-- Drop the index first
DROP INDEX IF EXISTS idx_chats_last_message_at;

-- SQLite doesn't support DROP COLUMN directly, so we need to recreate the table
-- However, for simplicity in a down migration, we'll just note that reverting
-- this migration requires database recreation or manual intervention
-- In practice, this column is additive and safe to leave
