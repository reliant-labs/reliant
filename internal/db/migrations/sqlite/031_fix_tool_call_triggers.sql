-- +goose Up
-- Fix tool_calls triggers to only include started_at and completed_at when they are actually set
-- This prevents sending contradictory status updates (e.g., status="executing" with completed_at set)

-- Drop existing tool_calls triggers from migration 029
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- Recreate tool_call insert trigger
-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',  -- Tool calls are part of message updates
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

-- Recreate tool_call update trigger with conditional timestamp fields
-- Only include started_at when it's set (not NULL)
-- Only include completed_at when it's set (not NULL)
-- This ensures status and timestamps are always consistent
-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status OR OLD.started_at != NEW.started_at OR OLD.completed_at != NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',  -- Tool calls are part of message updates
        NEW.message_id,
        CASE
            -- When completed (has completed_at), include all timestamps
            WHEN NEW.completed_at IS NOT NULL THEN
                json_object(
                    'update_type', 'tool_call',
                    'id', NEW.id,
                    'chat_id', NEW.chat_id,
                    'message_id', NEW.message_id,
                    'content_block_id', NEW.content_block_id,
                    'tool_name', NEW.tool_name,
                    'status', NEW.status,
                    'requested_at', NEW.requested_at,
                    'started_at', NEW.started_at,
                    'completed_at', NEW.completed_at,
                    'is_error', NEW.is_error
                )
            -- When executing (has started_at but no completed_at), include started_at
            WHEN NEW.started_at IS NOT NULL THEN
                json_object(
                    'update_type', 'tool_call',
                    'id', NEW.id,
                    'chat_id', NEW.chat_id,
                    'message_id', NEW.message_id,
                    'content_block_id', NEW.content_block_id,
                    'tool_name', NEW.tool_name,
                    'status', NEW.status,
                    'requested_at', NEW.requested_at,
                    'started_at', NEW.started_at
                )
            -- When pending (no started_at or completed_at), only include basic fields
            ELSE
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
        END
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Revert to migration 029 triggers

DROP TRIGGER IF EXISTS chat_updates_tool_call_update;
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',  -- Tool calls are part of message updates
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
        'message',  -- Tool calls are part of message updates
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
            'completed_at', NEW.completed_at,
            'is_error', NEW.is_error
        )
    );
END;
-- +goose StatementEnd
