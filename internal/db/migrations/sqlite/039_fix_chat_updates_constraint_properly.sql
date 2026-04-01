-- +goose Up
-- Fix CHECK constraint on chat_updates.update_type to allow new unified schema types
--
-- PROBLEM: Migration 038 changed triggers to emit 'workflow_execution', 'step_execution', and 'tool_call'
-- but the chat_updates table has a CHECK constraint that only allows the old types.
--
-- SOLUTION: Drop and recreate table with updated CHECK constraint (don't rename!)

-- Step 1: Drop all existing triggers first
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_event_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_update;
DROP TRIGGER IF EXISTS chat_updates_active_thread_insert;
DROP TRIGGER IF EXISTS chat_updates_active_thread_update;

-- Step 2: Create temporary table with data
CREATE TABLE chat_updates_backup AS SELECT * FROM chat_updates;

-- Step 3: Drop old table
DROP TABLE chat_updates;

-- Step 4: Create new table with updated CHECK constraint
CREATE TABLE chat_updates (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'tool_approval',
        'thread',
        'workflow_event',
        'workflow_approval',
        'workflow_execution',  -- NEW: unified schema
        'step_execution',      -- NEW: unified schema
        'tool_call'            -- NEW: unified schema
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- Step 5: Copy data back
INSERT INTO chat_updates SELECT * FROM chat_updates_backup;

-- Step 6: Drop backup
DROP TABLE chat_updates_backup;

-- Step 7: Recreate all triggers
-- Triggers from migrations 037 and 038 with correct update_type values
--
-- NOTE: We use DROP TRIGGER IF EXISTS even though dropping the table should have
-- dropped all triggers. This makes the migration idempotent in case it's interrupted
-- and re-run, preventing "trigger already exists" errors.

-- Message triggers
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_insert
AFTER INSERT ON messages
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'update_type', 'message',
            'id', NEW.id,
            'role', NEW.role,
            'ordinal', NEW.ordinal,
            'thread', NEW.thread,
            'context_sequence', NEW.context_sequence,
            'streaming_state', NEW.streaming_state,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_message_update;
-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_update
AFTER UPDATE ON messages
WHEN OLD.streaming_state != NEW.streaming_state
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'update_type', 'message',
            'id', NEW.id,
            'role', NEW.role,
            'ordinal', NEW.ordinal,
            'thread', NEW.thread,
            'context_sequence', NEW.context_sequence,
            'streaming_state', NEW.streaming_state,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- Workflow event trigger
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

-- Workflow execution triggers (migration 038 - with NEW types)
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_insert
AFTER INSERT ON workflow_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_execution',
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
        'workflow_execution',
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

-- Step execution triggers (migration 038 - with NEW types)
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_insert
AFTER INSERT ON step_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'step_execution',
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
        'step_execution',
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

-- Tool call triggers (migration 038 - with NEW types)
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_call_insert
AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_call',
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
    AND NOT (
        (OLD.status IN ('completed', 'failed', 'denied') AND NEW.status != OLD.status)
        OR (OLD.status = 'executing' AND NEW.status = 'pending')
    )
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_call',
        NEW.message_id,
        CASE
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

-- Workflow approval triggers
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

DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_update
AFTER UPDATE ON workflow_approvals
WHEN OLD.status != NEW.status
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

-- Tool approval triggers (legacy)
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

DROP TRIGGER IF EXISTS chat_updates_tool_approval_update;
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

-- Active thread triggers
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

DROP TRIGGER IF EXISTS chat_updates_active_thread_update;
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
-- Not reversible - requires careful data migration
