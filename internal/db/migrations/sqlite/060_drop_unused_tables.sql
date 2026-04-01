-- +goose Up
-- Drop unused tables that were never fully implemented or have been superseded

-- chat_state_cache: Interface methods exist but are stubs throwing "not yet implemented" errors
-- No actual callers exist - state is managed through Temporal workflow directly
DROP TABLE IF EXISTS chat_state_cache;

-- assistant_message_streams: Only referenced in test files, no business logic uses it
-- Streaming state is now computed from message_content_blocks (ComputeStreamingState function)
DROP TABLE IF EXISTS assistant_message_streams;

-- +goose Down
-- Recreate tables if needed (for rollback)

CREATE TABLE IF NOT EXISTS chat_state_cache (
    chat_id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    run_id TEXT,
    current_thread TEXT NOT NULL DEFAULT '0',
    current_context_sequence INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active',
        'waiting',
        'completed',
        'failed'
    )),
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_message_at DATETIME,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_chat_state_workflow ON chat_state_cache(workflow_id);
CREATE INDEX IF NOT EXISTS idx_chat_state_status ON chat_state_cache(status);

CREATE TABLE IF NOT EXISTS assistant_message_streams (
    message_id TEXT PRIMARY KEY,
    streaming_started_at DATETIME NOT NULL,
    streaming_completed_at DATETIME,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_assistant_streams_incomplete ON assistant_message_streams(message_id)
    WHERE streaming_completed_at IS NULL;
