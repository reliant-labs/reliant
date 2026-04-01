-- +goose Up
-- Migration: Add activity_id to messages for natural idempotency
-- Purpose: Every message created by an activity gets the activity_id
--          If activity retries and message already exists, no error - just continue
--          This eliminates all complex idempotency checking logic

-- Add activity_id to messages table
ALTER TABLE messages ADD COLUMN activity_id TEXT;

-- Create index for activity-based lookups
CREATE INDEX idx_messages_activity ON messages(activity_id)
WHERE activity_id IS NOT NULL;

-- Add unique constraint on (chat_id, thread, ordinal, activity_id)
-- This ensures one message per activity execution at a given position
-- If activity retries, it can check if its message already exists
CREATE UNIQUE INDEX idx_messages_activity_position ON messages(chat_id, thread, ordinal, activity_id)
WHERE activity_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_messages_activity_position;
DROP INDEX IF EXISTS idx_messages_activity;

-- Note: SQLite doesn't support DROP COLUMN directly in older versions
-- For rollback, activity_id column will remain but be unused
