-- +goose Up
-- Migration: Add workflow tables for DynamicWorkflow execution
-- These tables support the edge-centric YAML workflow system

-- workflow_executions tracks running/completed workflows
CREATE TABLE IF NOT EXISTS workflow_executions (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL, -- Temporal workflow ID
    run_id TEXT NOT NULL,      -- Temporal run ID

    -- What triggered this workflow
    workflow_name TEXT NOT NULL,
    trigger_event TEXT NOT NULL, -- PreToolUse, PostToolUse, etc.

    -- Related entities
    chat_id TEXT,
    message_id TEXT,
    tool_call_id TEXT,

    -- Workflow state
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'running',
        'completed',
        'failed',
        'cancelled',
        'timed_out'
    )),

    -- Input/output (JSON)
    input_data TEXT,
    output_data TEXT,
    error_message TEXT,

    -- Timing
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL,
    UNIQUE(workflow_id, run_id)
);

CREATE INDEX IF NOT EXISTS idx_workflow_executions_status ON workflow_executions(status, started_at);
CREATE INDEX IF NOT EXISTS idx_workflow_executions_chat ON workflow_executions(chat_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_executions_workflow_name ON workflow_executions(workflow_name, started_at DESC);

-- workflow_signals tracks signals sent to workflows
CREATE TABLE IF NOT EXISTS workflow_signals (
    id TEXT PRIMARY KEY,
    workflow_execution_id TEXT NOT NULL,
    signal_name TEXT NOT NULL,
    signal_data TEXT, -- JSON
    sent_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (workflow_execution_id) REFERENCES workflow_executions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workflow_signals_execution ON workflow_signals(workflow_execution_id, sent_at);

-- workflow_events stores events that trigger workflow steps via edges
CREATE TABLE IF NOT EXISTS workflow_events (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL, -- Which YAML workflow this event is for
    event_name TEXT NOT NULL, -- "tool.pre_use", "step.completed", "turn.complete", etc.
    event_data TEXT NOT NULL, -- JSON with event metadata (tool_name, step_id, output, etc.)

    -- Hierarchy (for nested workflows)
    step_path TEXT NOT NULL DEFAULT '', -- "build-feature.execute-tool" (dot-separated)
    parent_step_id TEXT, -- "build-feature" (for quick parent filtering)

    -- Approval (for blocking triggers)
    requires_approval BOOLEAN DEFAULT FALSE,
    approval_id TEXT, -- Links to workflow_approvals.id

    -- Processing state
    processed BOOLEAN DEFAULT FALSE,
    processed_at DATETIME,

    -- Metadata
    source_type TEXT, -- "root", "agent", "workflow_step"
    source_step_id TEXT, -- If from workflow step
    agent_name TEXT, -- If from agent

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (approval_id) REFERENCES workflow_approvals(id)
);

CREATE INDEX IF NOT EXISTS idx_workflow_events_lookup ON workflow_events(chat_id, workflow_name, processed, created_at);
CREATE INDEX IF NOT EXISTS idx_workflow_events_processing ON workflow_events(chat_id, processed, created_at);
CREATE INDEX IF NOT EXISTS idx_workflow_events_chat ON workflow_events(chat_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_events_approval ON workflow_events(approval_id) WHERE requires_approval = TRUE;
CREATE INDEX IF NOT EXISTS idx_workflow_events_step_path ON workflow_events(step_path);

-- workflow_approvals tracks approval state for blocking triggers
CREATE TABLE IF NOT EXISTS workflow_approvals (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
    denial_reason TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (event_id) REFERENCES workflow_events(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workflow_approvals_status ON workflow_approvals(status, updated_at);

-- workflow_step_outputs stores outputs from executed steps for CEL access
CREATE TABLE IF NOT EXISTS workflow_step_outputs (
    id TEXT PRIMARY KEY,
    workflow_execution_id TEXT NOT NULL,
    step_path TEXT NOT NULL, -- Full path including nested steps
    output_data TEXT NOT NULL, -- JSON output from activity/workflow
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(workflow_execution_id, step_path)
);

CREATE INDEX IF NOT EXISTS idx_workflow_step_outputs_lookup ON workflow_step_outputs(workflow_execution_id, step_path);

-- workflow_step_state tracks running/completed step state
CREATE TABLE IF NOT EXISTS workflow_step_state (
    id TEXT PRIMARY KEY,
    workflow_execution_id TEXT NOT NULL,  -- DynamicWorkflow execution ID
    step_id TEXT NOT NULL,                -- YAML step ID (user-visible)
    step_path TEXT NOT NULL DEFAULT '',   -- Full path for nested workflows
    thread_id TEXT,                       -- ThreadWorkflow's thread_id (internal)
    agent_name TEXT,
    status TEXT NOT NULL,                 -- 'running', 'completed', 'failed'

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    UNIQUE(workflow_execution_id, step_path)
);

CREATE INDEX IF NOT EXISTS idx_workflow_step_state_lookup ON workflow_step_state(workflow_execution_id, step_id);
CREATE INDEX IF NOT EXISTS idx_workflow_step_state_status ON workflow_step_state(workflow_execution_id, status);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_step_state_status;
DROP INDEX IF EXISTS idx_workflow_step_state_lookup;
DROP TABLE IF EXISTS workflow_step_state;

DROP INDEX IF EXISTS idx_workflow_step_outputs_lookup;
DROP TABLE IF EXISTS workflow_step_outputs;

DROP INDEX IF EXISTS idx_workflow_approvals_status;
DROP TABLE IF EXISTS workflow_approvals;

DROP INDEX IF EXISTS idx_workflow_events_step_path;
DROP INDEX IF EXISTS idx_workflow_events_approval;
DROP INDEX IF EXISTS idx_workflow_events_chat;
DROP INDEX IF EXISTS idx_workflow_events_processing;
DROP INDEX IF EXISTS idx_workflow_events_lookup;
DROP TABLE IF EXISTS workflow_events;

DROP INDEX IF EXISTS idx_workflow_signals_execution;
DROP TABLE IF EXISTS workflow_signals;

DROP INDEX IF EXISTS idx_workflow_executions_workflow_name;
DROP INDEX IF EXISTS idx_workflow_executions_chat;
DROP INDEX IF EXISTS idx_workflow_executions_status;
DROP TABLE IF EXISTS workflow_executions;

