-- +goose Up
-- Add active_daemon_id to chats for session-level daemon targeting.
-- When set, this daemon is used as the default for tool execution in this chat.
ALTER TABLE chats ADD COLUMN active_daemon_id TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN before 3.35.0, so we recreate without it.
-- For simplicity, we leave the column in place on downgrade.
