-- +goose Up
-- Create content_block_chunks table to support streaming content blocks by index
CREATE TABLE content_block_chunks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    block_index INTEGER NOT NULL,
    block_type TEXT NOT NULL CHECK (block_type IN ('text', 'tool_use', 'thinking')),
    chunk_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
    UNIQUE(message_id, block_index, chunk_index)
);

CREATE INDEX idx_content_block_chunks_message ON content_block_chunks(message_id, block_index, chunk_index);

-- NOTE: Trigger for enriching message updates with content_block_chunks
-- is created in migration 056_enrich_messages_at_write_time.sql

-- Add is_complete column to message_content_blocks to track when streaming finishes
ALTER TABLE message_content_blocks ADD COLUMN is_complete BOOLEAN DEFAULT false;

-- Add is_streaming column to messages to track streaming state
ALTER TABLE messages ADD COLUMN is_streaming BOOLEAN DEFAULT false;

-- +goose Down
DROP INDEX IF EXISTS idx_content_block_chunks_message;
DROP TABLE IF EXISTS content_block_chunks;

ALTER TABLE message_content_blocks DROP COLUMN is_complete;
ALTER TABLE messages DROP COLUMN is_streaming;
