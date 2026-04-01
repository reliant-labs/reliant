-- +goose Up
-- Unify websocket schema by fixing update_type column values
--
-- BUG: The "Thinking" indicator doesn't work because of a schema mismatch:
-- - Database triggers emit: update_type='workflow_event' with data.update_type='step_execution'
-- - Frontend filters for: update_type='step_execution' (never matches!)
-- - Result: Step execution updates never reach the frontend, busy state never updates
--
-- ROOT CAUSE: Inconsistent use of update_type at two levels:
-- 1. Table column: chat_updates.update_type (used for filtering)
-- 2. JSON field: data.update_type (used for parsing)
--
-- SOLUTION: Set the correct update_type in the table column to match the nested type.
-- This allows frontend to filter by update_type column and get the right events.
--
-- CHANGES:
-- - workflow_execution updates: update_type='workflow_event' -> 'workflow_execution'
-- - step_execution updates: update_type='workflow_event' -> 'step_execution'
-- - tool_call updates: update_type='message' -> 'tool_call'
-- - workflow_approval updates: Already correct ('workflow_approval')
-- - workflow_event updates: Already correct ('workflow_event')
-- - tool_approval updates: Already correct ('tool_approval')
-- - thread updates: Already correct ('thread')
-- - message updates: Keep as-is (need separate handling for streaming)
--
-- We preserve the nested data.update_type field for now (cleanup later).

-- ============================================================================
-- WORKFLOW_EXECUTION TRIGGERS
-- ============================================================================
-- Change: update_type from 'workflow_event' to 'workflow_execution'

DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_insert
AFTER INSERT ON workflow_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_execution',  -- FIXED: was 'workflow_event'
        NEW.id,
        json_object(
            'update_type', 'workflow_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'status', NEW.status,
            'started_at', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_update
AFTER UPDATE ON workflow_executions
WHEN OLD.status != NEW.status OR OLD.completed_at IS NOT NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_execution',  -- FIXED: was 'workflow_event'
        NEW.id,
        json_object(
            'update_type', 'workflow_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'updated_at', NEW.updated_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- ============================================================================
-- STEP_EXECUTION TRIGGERS
-- ============================================================================
-- Change: update_type from 'workflow_event' to 'step_execution'

DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_insert
AFTER INSERT ON step_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'step_execution',  -- FIXED: was 'workflow_event'
        NEW.id,
        json_object(
            'update_type', 'step_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_execution_id', NEW.workflow_execution_id,
            'step_id', NEW.step_id,
            'step_path', NEW.step_path,
            'status', NEW.status,
            'started_at', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_step_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_update
AFTER UPDATE ON step_executions
WHEN OLD.status != NEW.status
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'step_execution',  -- FIXED: was 'workflow_event'
        NEW.id,
        json_object(
            'update_type', 'step_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_execution_id', NEW.workflow_execution_id,
            'step_id', NEW.step_id,
            'step_path', NEW.step_path,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- ============================================================================
-- TOOL_CALL TRIGGERS
-- ============================================================================
-- Change: update_type from 'message' to 'tool_call'

DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_call',  -- FIXED: was 'message'
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

DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status
    -- Prevent regressive transitions (from migration 034)
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
        'tool_call',  -- FIXED: was 'message'
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
            -- When pending (no started_at), only include requested_at
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

-- ============================================================================
-- NOTE: Other triggers already use correct update_type values
-- ============================================================================
-- The following triggers already set update_type correctly:
-- - chat_updates_workflow_approval_insert: 'workflow_approval' ✓
-- - chat_updates_workflow_approval_update: 'workflow_approval' ✓
-- - chat_updates_workflow_event_insert: 'workflow_event' ✓
-- - chat_updates_tool_approval_insert: 'tool_approval' ✓
-- - chat_updates_tool_approval_update: 'tool_approval' ✓
-- - chat_updates_active_thread_insert: 'thread' ✓
-- - chat_updates_active_thread_update: 'thread' ✓
-- - chat_updates_message_insert: 'message' ✓
-- - chat_updates_message_update: 'message' ✓

-- +goose Down
-- Revert to migration 037 trigger definitions (with mismatched update_type values)

-- Restore workflow_execution triggers with 'workflow_event' update_type
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_insert
AFTER INSERT ON workflow_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',  -- Reverted to old value
        NEW.id,
        json_object(
            'update_type', 'workflow_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'status', NEW.status,
            'started_at', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_update
AFTER UPDATE ON workflow_executions
WHEN OLD.status != NEW.status OR OLD.completed_at IS NOT NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',  -- Reverted to old value
        NEW.id,
        json_object(
            'update_type', 'workflow_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'updated_at', NEW.updated_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- Restore step_execution triggers with 'workflow_event' update_type
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_insert
AFTER INSERT ON step_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',  -- Reverted to old value
        NEW.id,
        json_object(
            'update_type', 'step_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_execution_id', NEW.workflow_execution_id,
            'step_id', NEW.step_id,
            'step_path', NEW.step_path,
            'status', NEW.status,
            'started_at', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_step_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_update
AFTER UPDATE ON step_executions
WHEN OLD.status != NEW.status
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',  -- Reverted to old value
        NEW.id,
        json_object(
            'update_type', 'step_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_execution_id', NEW.workflow_execution_id,
            'step_id', NEW.step_id,
            'step_path', NEW.step_path,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- Restore tool_call triggers with 'message' update_type
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',  -- Reverted to old value
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

DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_update
AFTER UPDATE ON tool_calls
WHEN OLD.status != NEW.status
    -- Prevent regressive transitions (from migration 034)
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
        'message',  -- Reverted to old value
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
            -- When pending (no started_at), only include requested_at
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
