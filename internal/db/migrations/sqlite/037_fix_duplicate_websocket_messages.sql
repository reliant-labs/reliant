-- +goose Up
-- Fix duplicate and unnecessary websocket messages
--
-- This migration addresses several issues causing excessive websocket traffic:
-- 1. Message duplicates: content_block triggers + idempotent updates firing message UPDATE
-- 2. workflow_event processed field: broadcasting internal state changes
-- 3. workflow_execution updated_at: broadcasting timestamp-only updates
-- 4. step_execution updates: no deduplication on status-only changes
-- 5. Idempotent updates: triggers firing on updated_at changes even when no data changed

-- ============================================================================
-- ISSUE 1: Message duplication from content_block triggers
-- ============================================================================
-- Problem: Both message INSERT and content_block INSERT/UPDATE triggers create
-- chat_updates entries with update_type='message'. This causes each message to
-- appear 2x (message + 1 content block) or more (message + N content blocks).
--
-- Solution: Remove content_block triggers. The message triggers alone are sufficient.

DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- ============================================================================
-- ISSUE 1b: Message duplicates from idempotent updates
-- ============================================================================
-- Problem: The message UPDATE trigger fires when OLD.updated_at != NEW.updated_at.
-- Since every UPDATE sets updated_at = datetime('now', 'utc'), this means the
-- trigger fires even for idempotent updates that don't change any actual data.
-- This causes messages to be broadcast 2-4x when they're updated multiple times
-- with the same values.
--
-- Solution: Only fire trigger when streaming_state changes, not updated_at.

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

-- ============================================================================
-- ISSUE 2: workflow_event processed field updates
-- ============================================================================
-- Problem: The workflow_event UPDATE trigger fires on processed/processed_at changes.
-- These are internal state changes that don't need to be broadcast to clients.
-- Events are sent 3x: once on INSERT (processed=0), once when processed=1,
-- and again when processed_at is updated.
--
-- Solution: Remove the workflow_event UPDATE trigger entirely. Only broadcast
-- on INSERT - subsequent processed field changes are internal only.

DROP TRIGGER IF EXISTS chat_updates_workflow_event_update;

-- ============================================================================
-- ISSUE 3: workflow_execution updated_at broadcasts
-- ============================================================================
-- Problem: The workflow_execution UPDATE trigger fires on updated_at changes.
-- This causes broadcasts even when only timestamps change, not actual workflow state.
--
-- Solution: Only fire trigger on meaningful state changes (status or completed_at),
-- not on updated_at changes.

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
        'workflow_event',
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
-- ISSUE 4: step_execution duplicate updates
-- ============================================================================
-- Problem: step_execution UPDATE trigger fires on status OR completed_at changes.
-- This can cause duplicate updates when both change in the same UPDATE statement,
-- or when the status is set to the same value (idempotent updates).
--
-- Solution: Add status change check to prevent duplicate updates.

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
        'workflow_event',
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
-- ISSUE 5: workflow_approval idempotent updates
-- ============================================================================
-- Problem: The workflow_approval UPDATE trigger fires on updated_at OR status changes.
-- This causes duplicates when approvals are updated multiple times with timestamp-only changes.
--
-- Solution: Only fire trigger when status changes, not updated_at.

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

-- +goose Down
-- Revert to migration 032 triggers

-- Restore message UPDATE trigger with updated_at check
DROP TRIGGER IF EXISTS chat_updates_message_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_update
AFTER UPDATE ON messages
WHEN OLD.updated_at != NEW.updated_at OR OLD.streaming_state != NEW.streaming_state
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

-- Restore content_block triggers
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

-- Restore workflow_event update trigger
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

-- Restore workflow_execution update trigger with updated_at check
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_update
AFTER UPDATE ON workflow_executions
WHEN OLD.status != NEW.status
   OR OLD.completed_at IS NOT NEW.completed_at
   OR OLD.updated_at IS NOT NEW.updated_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
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

-- Restore step_execution update trigger with completed_at check
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_update
AFTER UPDATE ON step_executions
WHEN OLD.status != NEW.status OR OLD.completed_at != NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
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

-- Restore workflow_approval update trigger with updated_at check
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;

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
