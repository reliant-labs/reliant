-- +goose Up
-- Migration: Remove last_message_at column from chats table
-- We now compute this via JOIN on the messages table instead of storing it denormalized
-- This prevents the need to update the column on every message insert

-- Drop the index first
DROP INDEX IF EXISTS idx_chats_last_message_at;

-- SQLite 3.35+ supports ALTER TABLE DROP COLUMN
ALTER TABLE chats DROP COLUMN last_message_at;

-- +goose Down
-- Re-add the last_message_at column if needed

-- Add the column back
ALTER TABLE chats ADD COLUMN last_message_at TIMESTAMP;

-- Recreate the index
CREATE INDEX IF NOT EXISTS idx_chats_last_message_at ON chats(last_message_at DESC);

-- Repopulate from messages
UPDATE chats SET last_message_at = COALESCE(
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = chats.id),
    chats.created_at
);
