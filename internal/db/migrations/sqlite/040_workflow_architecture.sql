-- +goose Up
-- Add workflow architecture tables and node-based filtering

-- Drop old workflow tables from migration 019 (they're being replaced with new schema)
DROP TABLE IF EXISTS workflow_step_state;
DROP TABLE IF EXISTS workflow_step_outputs;
DROP TABLE IF EXISTS workflow_signals;
DROP TABLE IF EXISTS workflow_executions;

-- Add node filtering columns to messages (only if they don't exist)
-- Note: These columns may already exist from a previous migration attempt
-- SQLite will error if we try to add existing columns, so we skip this section
-- The columns should already exist: node_id, node_path

-- Create index only if it doesn't exist
CREATE INDEX IF NOT EXISTS idx_messages_node ON messages(chat_id, node_id, ordinal);

-- Add node filtering columns to message_content_blocks
-- Note: These columns may already exist from a previous migration attempt
-- SQLite will error if we try to add existing columns, so we skip this section
-- The columns should already exist: node_id, node_path

-- Tool calls table (extracted from content blocks for structured workflow tracking)
CREATE TABLE IF NOT EXISTS tool_calls (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    content_block_id TEXT NOT NULL UNIQUE,  -- Link to existing block

    node_id TEXT NOT NULL,
    node_path TEXT NOT NULL,

    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL,
    tool_call_id TEXT NOT NULL UNIQUE,

    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending', 'executing', 'completed', 'failed', 'denied'
    )),

    result_message_id TEXT,  -- Points to TOOL message with result
    result_content_block_id TEXT,  -- Points to tool_result block
    result_content TEXT,
    is_error BOOLEAN DEFAULT FALSE,

    requested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
    FOREIGN KEY (content_block_id) REFERENCES message_content_blocks(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_chat ON tool_calls(chat_id, status);
CREATE INDEX IF NOT EXISTS idx_tool_calls_node ON tool_calls(chat_id, node_id, status);
CREATE INDEX IF NOT EXISTS idx_tool_calls_message ON tool_calls(message_id);

-- Workflow executions table (top-level workflow tracking)
CREATE TABLE IF NOT EXISTS workflow_executions (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,

    parent_execution_id TEXT,
    root_node_id TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),

    error_message TEXT,

    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME,
    completed_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_execution_id) REFERENCES workflow_executions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workflow_exec_chat ON workflow_executions(chat_id, status);
CREATE INDEX IF NOT EXISTS idx_workflow_exec_parent ON workflow_executions(parent_execution_id);

-- Step executions table (individual workflow step tracking)
CREATE TABLE IF NOT EXISTS step_executions (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_execution_id TEXT NOT NULL,

    step_id TEXT NOT NULL,
    step_path TEXT NOT NULL,

    node_id TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),

    output_data TEXT,
    error_message TEXT,

    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_execution_id) REFERENCES workflow_executions(id) ON DELETE CASCADE,
    UNIQUE(workflow_execution_id, step_path)
);

CREATE INDEX IF NOT EXISTS idx_step_exec_chat ON step_executions(chat_id, node_id, status);
CREATE INDEX IF NOT EXISTS idx_step_exec_workflow ON step_executions(workflow_execution_id, status);

-- Add update_type values for new tables to chat_updates
-- Note: The chat_updates table already exists from migration 028
-- We just need to add triggers for the new tables

-- Drop existing triggers if they exist (in case of retry)
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;

-- Triggers for tool_calls table
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

-- Triggers for workflow_executions table
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_insert
AFTER INSERT ON workflow_executions
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
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_update
AFTER UPDATE ON workflow_executions
WHEN OLD.status != NEW.status OR OLD.completed_at IS NOT NEW.completed_at OR OLD.updated_at IS NOT NEW.updated_at
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

-- Triggers for step_executions table
-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_insert
AFTER INSERT ON step_executions
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
            'started_at', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

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

-- +goose Down
-- Drop triggers
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_call_update;
DROP TRIGGER IF EXISTS chat_updates_tool_call_insert;

-- Drop tables
DROP TABLE IF EXISTS step_executions;
DROP TABLE IF EXISTS workflow_executions;
DROP TABLE IF EXISTS tool_calls;

-- Drop indexes
DROP INDEX IF EXISTS idx_messages_node;

-- Remove columns from messages
-- Note: SQLite doesn't support DROP COLUMN directly, so we would need to recreate the table
-- For now, we'll leave the columns in place during rollback to avoid data loss
-- If a full rollback is needed, the columns will remain but be unused

-- Remove columns from message_content_blocks
-- Same limitation as above - columns will remain but be unused
