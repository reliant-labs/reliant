-- +goose Up
-- Prevent regressive tool_call state transitions by checking before firing trigger
-- This fixes the issue where status can go backwards (completed -> executing)
--
-- The problem: Migration 033 only checks if status changed, not if it's a valid transition
-- A tool call should only move forward: pending -> executing -> completed/failed/denied
--
-- The solution: Add guards in the trigger WHEN clause to prevent regressive transitions

DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- Recreate tool_call update trigger with guards against regressive transitions
-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status
    -- Prevent regressive transitions
    AND NOT (
        -- Can't go backwards from terminal states
        (OLD.status IN ('completed', 'failed', 'denied') AND NEW.status != OLD.status)
        -- Can't go from executing back to pending
        OR (OLD.status = 'executing' AND NEW.status = 'pending')
    )
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
-- Revert to migration 033 trigger (without regressive transition guards)

DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status
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
