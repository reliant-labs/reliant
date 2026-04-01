-- +goose Up
-- Migration: Unified step_executions table
-- Purpose: Track ALL activity executions (both workflow steps AND standalone activities)
--
-- UNIFIED MODEL:
-- Every Temporal activity execution gets ONE record in this table, identified by:
--   (workflow_id, activity_id) = PRIMARY KEY
--
-- This replaces the dual concepts of:
--   1. "step_executions" (workflow steps only)
--   2. "activity_executions" (standalone activities)
--
-- Key design decisions:
--   - activity_id is Temporal's unique identifier for an activity execution
--   - attempt_number tracks Temporal retries (1, 2, 3, ...)
--   - step_id/step_path are NULL for standalone activities (e.g., CreateWorkflowExecution)
--   - activity_type distinguishes activities (e.g., "ExecuteTool", "ProcessMessage", "WorkflowStep")
--   - display_name provides UI-friendly labels
--
-- IDEMPOTENCY:
--   - INSERT OR REPLACE ensures retries update the same record
--   - Only ONE record exists per (workflow_id, activity_id)
--   - Retries increment attempt_number but maintain same activity_id

-- Drop the old step_executions table and its triggers
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;
DROP TABLE IF EXISTS step_executions;

-- Create the unified step_executions table
CREATE TABLE step_executions (
    -- Primary key: Ensures ONE record per activity execution
    workflow_id TEXT NOT NULL,
    activity_id TEXT NOT NULL,

    -- Temporal execution context
    run_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL DEFAULT 1,

    -- Chat context
    chat_id TEXT NOT NULL,

    -- Activity classification
    activity_type TEXT NOT NULL,  -- "ExecuteTool", "ProcessMessage", "WorkflowStep", etc.
    display_name TEXT NOT NULL,   -- UI-friendly name (e.g., "Execute Bash", "Process message")

    -- Workflow step context (NULL for standalone activities)
    step_id TEXT,                 -- Step ID from workflow graph (e.g., "execute-tool")
    step_path TEXT,               -- Hierarchical path (e.g., "build-feature.execute-tool")
    workflow_execution_id TEXT,   -- Parent workflow_executions.id (NULL for root activities)

    -- Node context (for frontend filtering)
    node_id TEXT,

    -- Execution status
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'running',
        'completed',
        'failed',
        'cancelled'
    )),

    -- Results
    output_data TEXT,             -- JSON output (for successful completion)
    error_message TEXT,           -- Error details (for failures)

    -- Timing
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    -- Metadata (JSON for extensibility)
    metadata TEXT,                -- Additional context (e.g., tool_name, message_id)

    -- Primary key ensures only ONE record per activity
    PRIMARY KEY (workflow_id, activity_id),

    -- Foreign keys
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_execution_id) REFERENCES workflow_executions(id) ON DELETE CASCADE
);

-- Indexes for common queries
CREATE INDEX idx_step_exec_chat ON step_executions(chat_id, status);
CREATE INDEX idx_step_exec_workflow ON step_executions(workflow_id, status);
CREATE INDEX idx_step_exec_workflow_exec ON step_executions(workflow_execution_id, status)
    WHERE workflow_execution_id IS NOT NULL;
CREATE INDEX idx_step_exec_activity_type ON step_executions(chat_id, activity_type, started_at DESC);
CREATE INDEX idx_step_exec_node ON step_executions(chat_id, node_id, status)
    WHERE node_id IS NOT NULL;

-- Triggers for chat_updates (websocket streaming)
-- +goose StatementBegin
CREATE TRIGGER chat_updates_step_exec_insert
AFTER INSERT ON step_executions
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'step_execution',
        NEW.activity_id,  -- Use activity_id as entity_id
        json_object(
            'update_type', 'step_execution',
            'workflow_id', NEW.workflow_id,
            'activity_id', NEW.activity_id,
            'run_id', NEW.run_id,
            'attempt_number', NEW.attempt_number,
            'chat_id', NEW.chat_id,
            'activity_type', NEW.activity_type,
            'display_name', NEW.display_name,
            'step_id', NEW.step_id,
            'step_path', NEW.step_path,
            'workflow_execution_id', NEW.workflow_execution_id,
            'node_id', NEW.node_id,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'metadata', NEW.metadata,
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
            'timestamp', NEW.started_at
        )
    );
END;
-- +goose StatementEnd

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
        NEW.activity_id,  -- Use activity_id as entity_id
        json_object(
            'update_type', 'step_execution',
            'workflow_id', NEW.workflow_id,
            'activity_id', NEW.activity_id,
            'run_id', NEW.run_id,
            'attempt_number', NEW.attempt_number,
            'chat_id', NEW.chat_id,
            'activity_type', NEW.activity_type,
            'display_name', NEW.display_name,
            'step_id', NEW.step_id,
            'step_path', NEW.step_path,
            'workflow_execution_id', NEW.workflow_execution_id,
            'node_id', NEW.node_id,
            'status', NEW.status,
            'output_data', NEW.output_data,
            'error_message', NEW.error_message,
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'metadata', NEW.metadata,
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
            'timestamp', COALESCE(NEW.completed_at, NEW.started_at)
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Drop triggers
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;

-- Drop indexes
DROP INDEX IF EXISTS idx_step_exec_node;
DROP INDEX IF EXISTS idx_step_exec_activity_type;
DROP INDEX IF EXISTS idx_step_exec_workflow_exec;
DROP INDEX IF EXISTS idx_step_exec_workflow;
DROP INDEX IF EXISTS idx_step_exec_chat;

-- Drop table
DROP TABLE IF EXISTS step_executions;

-- Restore old step_executions table (from migration 029)
CREATE TABLE step_executions (
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

CREATE INDEX idx_step_exec_chat ON step_executions(chat_id, node_id, status);
CREATE INDEX idx_step_exec_workflow ON step_executions(workflow_execution_id, status);

-- Restore old triggers
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
            'node_id', NEW.node_id,
            'status', NEW.status,
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
            'timestamp', NEW.started_at,
            'started_at', NEW.started_at
        )
    );
END;

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
            'node_id', NEW.node_id,
            'status', NEW.status,
            'sequence_number', COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
            'timestamp', COALESCE(NEW.completed_at, NEW.started_at),
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
