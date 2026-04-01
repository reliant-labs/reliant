-- +goose Up
-- Remove duplicate tool_call triggers
--
-- REASONING:
-- Tool calls are already pushed to chat_updates via content_block triggers.
-- The tool_calls table is an internal state machine for tracking tool execution status.
-- The UI gets tool calls from message_content_blocks, not from tool_calls table directly.
--
-- This prevents duplicate WebSocket updates for the same tool call:
-- - Once as content_block (with tool_call data)
-- - Once as tool_call (status tracking)
--
-- Tool execution status is now tracked via step_executions.output_data,
-- which is pushed through step_execution triggers.

DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- +goose Down
-- Restore tool_call triggers (creates duplication with content_block triggers)

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.message_id,
        json_object(
            'update_type', 'tool_call',
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status OR OLD.completed_at != NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.message_id,
        json_object(
            'update_type', 'tool_call',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'message_id', NEW.message_id,
            'content_block_id', NEW.content_block_id,
            'tool_name', NEW.tool_name,
            'status', NEW.status,
            'requested_at', NEW.requested_at,
            'completed_at', NEW.completed_at
        )
    );
END;
-- +goose StatementEnd
