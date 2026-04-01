-- +goose Up
-- Migration to consolidate approval tables and remove Temporal-redundant tables
-- Rationale: Temporal already handles workflow state, activity idempotency, and retry logic.
-- We're removing redundant tracking and consolidating approvals into a single table.

-- ============================================================================
-- STEP 1: Create consolidated approvals table
-- ============================================================================
-- This replaces both tool_approvals and workflow_approvals with a unified structure
CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,

    -- Type discriminator: 'tool' for tool calls, 'workflow_step' for workflow steps
    approval_type TEXT NOT NULL CHECK (approval_type IN ('tool', 'workflow_step')),

    -- Entity reference: content_block_id for tools, event_id for workflows
    entity_id TEXT NOT NULL UNIQUE,

    -- Approval state
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
    denial_reason TEXT,

    -- Display fields
    title TEXT NOT NULL DEFAULT '',
    description TEXT,

    -- Optional fields (JSON for flexibility)
    actions TEXT,  -- JSON array of available actions
    metadata TEXT, -- JSON object with type-specific metadata

    -- Timestamps
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE INDEX idx_approvals_chat_status ON approvals(chat_id, status);
CREATE INDEX idx_approvals_entity ON approvals(entity_id);
CREATE INDEX idx_approvals_type ON approvals(approval_type, status);
CREATE INDEX idx_approvals_created ON approvals(created_at DESC);

-- ============================================================================
-- STEP 2: Migrate existing approval data
-- ============================================================================

-- Migrate tool_approvals (approval_type = 'tool', entity_id = content_block_id)
INSERT INTO approvals (
    id, chat_id, approval_type, entity_id, status, denial_reason,
    title, description, actions, metadata, created_at, resolved_at
)
SELECT
    id,
    chat_id,
    'tool' as approval_type,
    content_block_id as entity_id,
    status,
    denial_reason,
    'Tool Approval' as title,
    NULL as description,
    NULL as actions,
    json_object('content_block_id', content_block_id) as metadata,
    created_at,
    responded_at as resolved_at
FROM tool_approvals;

-- Migrate workflow_approvals (approval_type = 'workflow_step', entity_id = event_id)
INSERT INTO approvals (
    id, chat_id, approval_type, entity_id, status, denial_reason,
    title, description, actions, metadata, created_at, resolved_at
)
SELECT
    id,
    COALESCE(chat_id, '') as chat_id,
    'workflow_step' as approval_type,
    event_id as entity_id,
    status,
    denial_reason,
    COALESCE(title, 'Workflow Approval') as title,
    description,
    actions,
    CASE
        WHEN metadata IS NOT NULL THEN metadata
        ELSE json_object(
            'workflow_id', workflow_id,
            'run_id', run_id,
            'step_id', step_id,
            'event_id', event_id
        )
    END as metadata,
    created_at,
    resolved_at
FROM workflow_approvals
WHERE chat_id IS NOT NULL AND chat_id != '';

-- ============================================================================
-- STEP 3: Drop old approval tables
-- ============================================================================
DROP TABLE IF EXISTS tool_approvals;
DROP TABLE IF EXISTS workflow_approvals;

-- ============================================================================
-- STEP 4: Drop Temporal-redundant tables
-- ============================================================================
-- These tables duplicate state that Temporal already tracks

-- Drop workflow_events table (workflow state should be in Temporal)
-- Events should be written directly to chat_updates for UI notifications
DROP TABLE IF EXISTS workflow_events;

-- Drop step_executions table (Temporal handles activity idempotency & retry)
-- UI can query Temporal's workflow history for execution details
DROP TABLE IF EXISTS step_executions;

-- Drop workflow_executions table (Temporal tracks workflow execution state)
-- UI can query Temporal's workflow status API
DROP TABLE IF EXISTS workflow_executions;

-- ============================================================================
-- STEP 5: Update chat_updates table to remove obsolete update types
-- ============================================================================
-- Remove the check constraint and recreate with updated types
-- We're removing: workflow_event, workflow_approval (now just 'approval')
-- We're removing: workflow_execution, step_execution (Temporal handles this)

-- First drop all existing triggers on chat_updates to avoid errors during table recreation
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- Create new table with updated constraint
CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

-- Copy existing data, mapping old types to new
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT
    id,
    chat_id,
    sequence_number,
    CASE
        WHEN update_type IN ('tool_approval', 'workflow_approval') THEN 'approval'
        WHEN update_type IN ('workflow_execution', 'step_execution') THEN 'message'  -- Convert to message updates
        ELSE update_type
    END as update_type,
    entity_id,
    data,
    created_at
FROM chat_updates
WHERE update_type IN ('message', 'approval', 'thread', 'tool_call', 'tool_approval', 'workflow_approval', 'workflow_execution', 'step_execution');

-- Drop old table and rename new one
DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- +goose Down
-- Note: This migration is not easily reversible due to data consolidation
-- We would need to split the approvals table back into tool_approvals and workflow_approvals
-- and recreate the Temporal-redundant tables. This is not implemented as it's a one-way
-- simplification migration.

SELECT 'This migration cannot be automatically reversed. Manual intervention required.' as error;
