-- +goose Up
-- Add conversation recovery tracking

-- Assistant message streams table to track streaming completion
CREATE TABLE IF NOT EXISTS assistant_message_streams (
    message_id TEXT PRIMARY KEY,
    streaming_started_at DATETIME NOT NULL,
    streaming_completed_at DATETIME,  -- NULL = interrupted, non-NULL = complete

    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_assistant_streams_incomplete ON assistant_message_streams(message_id)
    WHERE streaming_completed_at IS NULL;

-- Add cancellation_reason to tool_calls table
ALTER TABLE tool_calls ADD COLUMN cancellation_reason TEXT;

-- +goose Down
-- Drop assistant_message_streams table
DROP TABLE IF EXISTS assistant_message_streams;

-- Remove cancellation_reason from tool_calls
-- Note: SQLite doesn't support DROP COLUMN directly
-- The column will remain but be unused during rollback
