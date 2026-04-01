-- +goose Up
-- Fix chat_updates triggers to include all required fields for frontend interfaces
-- This fixes the websocket parse errors caused by missing required fields

-- 1. Fix workflow_event triggers
-- Drop and recreate workflow_event triggers with all required fields
DROP TRIGGER IF EXISTS chat_updates_workflow_event_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_event_insert
AFTER INSERT ON workflow_events
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
        NEW.id,
        json_object(
            'update_type', 'workflow_event',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'event_name', NEW.event_name,
            'event_data', NEW.event_data,
            'step_path', NEW.step_path,
            'parent_step_id', NEW.parent_step_id,
            'requires_approval', NEW.requires_approval,
            'approval_id', NEW.approval_id,
            'processed', NEW.processed,
            'processed_at', NEW.processed_at,
            'source_type', NEW.source_type,
            'source_step_id', NEW.source_step_id,
            'agent_name', NEW.agent_name,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- Add update trigger for workflow_events (currently missing)
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_event_update
AFTER UPDATE ON workflow_events
WHEN OLD.processed != NEW.processed OR OLD.processed_at != NEW.processed_at OR OLD.approval_id != NEW.approval_id
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
        NEW.id,
        json_object(
            'update_type', 'workflow_event',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'event_name', NEW.event_name,
            'event_data', NEW.event_data,
            'step_path', NEW.step_path,
            'parent_step_id', NEW.parent_step_id,
            'requires_approval', NEW.requires_approval,
            'approval_id', NEW.approval_id,
            'processed', NEW.processed,
            'processed_at', NEW.processed_at,
            'source_type', NEW.source_type,
            'source_step_id', NEW.source_step_id,
            'agent_name', NEW.agent_name,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- 2. Fix workflow_approval triggers
-- Drop and recreate workflow_approval triggers with all required fields
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_insert
AFTER INSERT ON workflow_approvals
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_approval',
        NEW.id,
        json_object(
            'update_type', 'workflow_approval',
            'id', NEW.id,
            'event_id', NEW.event_id,
            'chat_id', NEW.chat_id,
            'step_id', NEW.step_id,
            'status', NEW.status,
            'denial_reason', NEW.denial_reason,
            'title', NEW.title,
            'description', NEW.description,
            'timeout_duration', NEW.timeout_duration,
            'actions', NEW.actions,
            'metadata', NEW.metadata,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_update
AFTER UPDATE ON workflow_approvals
WHEN OLD.updated_at != NEW.updated_at OR OLD.status != NEW.status
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_approval',
        NEW.id,
        json_object(
            'update_type', 'workflow_approval',
            'id', NEW.id,
            'event_id', NEW.event_id,
            'chat_id', NEW.chat_id,
            'step_id', NEW.step_id,
            'status', NEW.status,
            'denial_reason', NEW.denial_reason,
            'title', NEW.title,
            'description', NEW.description,
            'timeout_duration', NEW.timeout_duration,
            'actions', NEW.actions,
            'metadata', NEW.metadata,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- 3. Fix tool_approval triggers
-- Drop and recreate tool_approval triggers with all required fields
DROP TRIGGER IF EXISTS chat_updates_tool_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_approval_insert
AFTER INSERT ON tool_approvals
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_approval',
        NEW.id,
        json_object(
            'update_type', 'tool_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'content_block_id', NEW.content_block_id,
            'status', NEW.status,
            'denial_reason', NEW.denial_reason,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at,
            'responded_at', NEW.responded_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_approval_update
AFTER UPDATE ON tool_approvals
WHEN OLD.updated_at != NEW.updated_at OR OLD.status != NEW.status
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_approval',
        NEW.id,
        json_object(
            'update_type', 'tool_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'content_block_id', NEW.content_block_id,
            'status', NEW.status,
            'denial_reason', NEW.denial_reason,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at,
            'responded_at', NEW.responded_at
        )
    );
END;
-- +goose StatementEnd

-- 4. Fix content_block triggers
-- Drop and recreate content_block triggers to include all message fields
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_insert
AFTER INSERT ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    SELECT
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        m.id,
        json_object(
            'update_type', 'message',
            'id', m.id,
            'role', m.role,
            'ordinal', m.ordinal,
            'thread', m.thread,
            'context_sequence', m.context_sequence,
            'streaming_state', m.streaming_state,
            'created_at', m.created_at,
            'updated_at', m.updated_at,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_update
AFTER UPDATE ON message_content_blocks
WHEN OLD.updated_at != NEW.updated_at OR OLD.streaming_state != NEW.streaming_state
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    SELECT
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        m.id,
        json_object(
            'update_type', 'message',
            'id', m.id,
            'role', m.role,
            'ordinal', m.ordinal,
            'thread', m.thread,
            'context_sequence', m.context_sequence,
            'streaming_state', m.streaming_state,
            'created_at', m.created_at,
            'updated_at', m.updated_at,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- 5. Fix active_thread triggers
-- Drop and recreate active_thread triggers with all required fields
DROP TRIGGER IF EXISTS chat_updates_active_thread_insert;
DROP TRIGGER IF EXISTS chat_updates_active_thread_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_active_thread_insert
AFTER INSERT ON active_threads
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'thread',
        NEW.id,
        json_object(
            'update_type', 'thread',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'thread', NEW.thread,
            'workflow_id', NEW.workflow_id,
            'run_id', NEW.run_id,
            'agent_name', NEW.agent_name,
            'is_planning_mode', NEW.is_planning_mode,
            'status', NEW.status,
            'current_activity', NEW.current_activity,
            'current_activity_started_at', NEW.current_activity_started_at,
            'spawned_by_tool_call_id', NEW.spawned_by_tool_call_id,
            'created_at', NEW.created_at,
            'completed_at', NEW.completed_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_active_thread_update
AFTER UPDATE ON active_threads
WHEN OLD.status != NEW.status OR OLD.current_activity != NEW.current_activity OR OLD.completed_at != NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'thread',
        NEW.id,
        json_object(
            'update_type', 'thread',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'thread', NEW.thread,
            'workflow_id', NEW.workflow_id,
            'run_id', NEW.run_id,
            'agent_name', NEW.agent_name,
            'is_planning_mode', NEW.is_planning_mode,
            'status', NEW.status,
            'current_activity', NEW.current_activity,
            'current_activity_started_at', NEW.current_activity_started_at,
            'spawned_by_tool_call_id', NEW.spawned_by_tool_call_id,
            'created_at', NEW.created_at,
            'completed_at', NEW.completed_at
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Revert to original triggers from migration 028

DROP TRIGGER IF EXISTS chat_updates_active_thread_update;
DROP TRIGGER IF EXISTS chat_updates_active_thread_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_update;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_event_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_event_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_event_insert
AFTER INSERT ON workflow_events
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
        NEW.id,
        json_object(
            'update_type', 'workflow_event',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'event_name', NEW.event_name,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_insert
AFTER INSERT ON workflow_approvals
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_approval',
        NEW.id,
        json_object(
            'update_type', 'workflow_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_update
AFTER UPDATE ON workflow_approvals
WHEN OLD.updated_at != NEW.updated_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_approval',
        NEW.id,
        json_object(
            'update_type', 'workflow_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_insert
AFTER INSERT ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    SELECT
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        m.id,
        json_object(
            'update_type', 'message',
            'id', m.id,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_update
AFTER UPDATE ON message_content_blocks
WHEN OLD.updated_at != NEW.updated_at OR OLD.streaming_state != NEW.streaming_state
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    SELECT
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        m.id,
        json_object(
            'update_type', 'message',
            'id', m.id,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_approval_insert
AFTER INSERT ON tool_approvals
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_approval',
        NEW.id,
        json_object(
            'update_type', 'tool_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'content_block_id', NEW.content_block_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_approval_update
AFTER UPDATE ON tool_approvals
WHEN OLD.updated_at != NEW.updated_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_approval',
        NEW.id,
        json_object(
            'update_type', 'tool_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'content_block_id', NEW.content_block_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_active_thread_insert
AFTER INSERT ON active_threads
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'thread',
        NEW.id,
        json_object(
            'update_type', 'thread',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'thread', NEW.thread,
            'status', NEW.status,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_active_thread_update
AFTER UPDATE ON active_threads
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'thread',
        NEW.id,
        json_object(
            'update_type', 'thread',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'thread', NEW.thread,
            'status', NEW.status,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd
