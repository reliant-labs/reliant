-- +goose Up
-- Migration 054: Add unique constraint on activity_id
-- This prevents duplicate messages on activity retries

CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_activity_id_unique
ON messages(chat_id, activity_id)
WHERE activity_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_messages_activity_id_unique;
