-- +goose Up
-- Prevent duplicate tool_call updates by only firing trigger when status actually changes
-- This fixes the issue where regressive state transitions (completed -> executing) were being sent
--
-- The problem: The existing trigger fires on ANY change to status, started_at, or completed_at.
-- When UpdateToolCallStatus is called, it may update timestamps even if status hasn't changed,
-- causing duplicate chat_updates to be emitted.
--
-- The solution: Only fire the trigger when status actually changes. Timestamp updates alone
-- should not trigger a new chat_update.

-- Drop existing tool_calls update trigger from migration 031
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- Recreate tool_call update trigger that ONLY fires when status changes
-- This ensures each tool call gets exactly ONE update per state transition
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

-- +goose Down
-- Revert to migration 031 trigger (with timestamp checks)

DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

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
