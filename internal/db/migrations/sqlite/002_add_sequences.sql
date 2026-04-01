-- +goose Up
-- Add sequence tracking for WebSocket synchronization

-- Add per-chat sequence counter for distributed scalability
ALTER TABLE chats ADD COLUMN current_sequence INTEGER NOT NULL DEFAULT 0;

-- Add sequence column to messages
ALTER TABLE messages ADD COLUMN sequence INTEGER;
CREATE INDEX IF NOT EXISTS idx_messages_sequence ON messages(sequence);
CREATE INDEX IF NOT EXISTS idx_messages_chat_sequence ON messages(chat_id, sequence);

-- Add sequence column to message_content_blocks (for streaming deltas)
ALTER TABLE message_content_blocks ADD COLUMN sequence INTEGER;
CREATE INDEX IF NOT EXISTS idx_content_blocks_sequence ON message_content_blocks(sequence);
CREATE INDEX IF NOT EXISTS idx_content_blocks_message_sequence ON message_content_blocks(message_id, sequence);

-- +goose Down
-- Remove sequence tracking

DROP INDEX IF EXISTS idx_content_blocks_message_sequence;
DROP INDEX IF EXISTS idx_content_blocks_sequence;
DROP INDEX IF EXISTS idx_messages_chat_sequence;
DROP INDEX IF EXISTS idx_messages_sequence;

-- Note: SQLite doesn't support DROP COLUMN, so we can't remove the sequence columns
-- In a real migration rollback, you'd need to recreate the tables
