-- +goose Up
-- Add active_daemon_id to chats for session-level daemon targeting.
-- When set, this daemon is used as the default for tool execution in this chat.
ALTER TABLE chats ADD COLUMN active_daemon_id TEXT;

-- +goose Down
ALTER TABLE chats DROP COLUMN active_daemon_id;
