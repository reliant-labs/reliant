-- +goose Up
-- Drop tool_calls table - V2 workflows store tool calls as content blocks in messages
-- This table was used in V1 but is no longer written to by any production code path.
-- Tool call information is now stored in message_content_blocks with block_type='tool_call'

-- First drop the triggers that write to chat_updates
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- Drop the table
DROP TABLE IF EXISTS tool_calls;

-- +goose Down
-- Recreate tool_calls table if needed (for rollback)
CREATE TABLE IF NOT EXISTS tool_calls (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    content_block_id TEXT NOT NULL UNIQUE,

    node_id TEXT NOT NULL,
    node_path TEXT NOT NULL,

    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL,
    tool_call_id TEXT NOT NULL UNIQUE,

    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending', 'executing', 'completed', 'failed', 'denied'
    )),

    result_message_id TEXT,
    result_content_block_id TEXT,
    result_content TEXT,
    is_error BOOLEAN DEFAULT FALSE,

    requested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME,
    cancellation_reason TEXT,
    activity_id TEXT,
    workflow_run_id TEXT,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
    FOREIGN KEY (content_block_id) REFERENCES message_content_blocks(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_chat ON tool_calls(chat_id, status);
CREATE INDEX IF NOT EXISTS idx_tool_calls_node ON tool_calls(chat_id, node_id, status);
CREATE INDEX IF NOT EXISTS idx_tool_calls_message ON tool_calls(message_id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_activity ON tool_calls(activity_id) WHERE activity_id IS NOT NULL;

-- Recreate triggers
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_call',
        NEW.id,
        json_object(
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'message_id', NEW.message_id,
            'content_block_id', NEW.content_block_id,
            'tool_name', NEW.tool_name,
            'status', NEW.status,
            'requested_at', NEW.requested_at
        )
    );
END;

CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status OR OLD.completed_at != NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_call',
        NEW.id,
        json_object(
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'message_id', NEW.message_id,
            'content_block_id', NEW.content_block_id,
            'tool_name', NEW.tool_name,
            'status', NEW.status,
            'requested_at', NEW.requested_at,
            'completed_at', NEW.completed_at,
            'is_error', NEW.is_error
        )
    );
END;
