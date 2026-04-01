-- +goose Up
-- Add missing fields to workflow_execution updates
--
-- BUG: workflow_execution WebSocket updates are missing root_node_id and sequence_number
-- which the frontend expects (see WorkflowExecutionUpdate interface in websocket.ts)
--
-- IMPACT:
-- - TypeScript interface expects root_node_id and sequence_number
-- - Database triggers don't include these fields
-- - This could cause parsing errors or incorrect state tracking
--
-- FIX: Add root_node_id and sequence_number to the JSON payload

-- ============================================================================
-- WORKFLOW_EXECUTION TRIGGERS - ADD MISSING FIELDS
-- ============================================================================

DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;

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
            'root_node_id', NEW.root_node_id,  -- ADDED: frontend expects this
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,  -- ADDED: frontend expects this
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
            'root_node_id', NEW.root_node_id,  -- ADDED: frontend expects this
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,  -- ADDED: frontend expects this
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- ============================================================================
-- STEP_EXECUTION TRIGGERS - ADD MISSING FIELDS
-- ============================================================================
-- Also add node_id and sequence_number to step_execution updates for consistency

DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;

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
            'node_id', NEW.node_id,  -- ADDED: frontend expects this
            'status', NEW.status,
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,  -- ADDED: frontend expects this
            'timestamp', NEW.started_at,  -- ADDED: frontend expects 'timestamp' not 'started_at'
            'started_at', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_step_exec_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_update
AFTER UPDATE ON step_executions
WHEN OLD.status != NEW.status OR OLD.completed_at IS NOT NEW.completed_at
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
            'node_id', NEW.node_id,  -- ADDED: frontend expects this
            'status', NEW.status,
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,  -- ADDED: frontend expects this
            'timestamp', COALESCE(NEW.completed_at, NEW.started_at),  -- ADDED: frontend expects 'timestamp'
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Revert to migration 045 trigger definitions (without the added fields)

DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;

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
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;

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
WHEN OLD.status != NEW.status OR OLD.completed_at IS NOT NEW.completed_at
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
