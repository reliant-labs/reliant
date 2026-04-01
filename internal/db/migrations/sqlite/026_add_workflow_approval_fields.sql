-- +goose Up
-- Migration: Add missing fields to workflow_approvals table
-- The workflow_approvals table was missing fields that are used by the code

-- Add missing columns to workflow_approvals
ALTER TABLE workflow_approvals ADD COLUMN chat_id TEXT;
ALTER TABLE workflow_approvals ADD COLUMN workflow_id TEXT;
ALTER TABLE workflow_approvals ADD COLUMN run_id TEXT;
ALTER TABLE workflow_approvals ADD COLUMN step_id TEXT;
ALTER TABLE workflow_approvals ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_approvals ADD COLUMN description TEXT;
ALTER TABLE workflow_approvals ADD COLUMN timeout_duration TEXT;
ALTER TABLE workflow_approvals ADD COLUMN actions TEXT;
ALTER TABLE workflow_approvals ADD COLUMN metadata TEXT;
ALTER TABLE workflow_approvals ADD COLUMN resolved_at DATETIME;

-- Backfill chat_id from workflow_events via event_id
UPDATE workflow_approvals 
SET chat_id = (
    SELECT we.chat_id 
    FROM workflow_events we 
    WHERE we.id = workflow_approvals.event_id
)
WHERE chat_id IS NULL;

-- Backfill workflow_id from workflow_events
UPDATE workflow_approvals 
SET workflow_id = (
    SELECT we.workflow_id 
    FROM workflow_events we 
    WHERE we.id = workflow_approvals.event_id
)
WHERE workflow_id IS NULL;

-- Backfill run_id from workflow_events
UPDATE workflow_approvals 
SET run_id = (
    SELECT we.run_id 
    FROM workflow_events we 
    WHERE we.id = workflow_approvals.event_id
)
WHERE run_id IS NULL;

-- Add foreign key constraint for chat_id (SQLite requires recreate for FK)
-- Note: SQLite doesn't support ADD CONSTRAINT, so we document this for reference
-- The FK will be enforced in the application layer

-- Add index for chat_id lookups
CREATE INDEX IF NOT EXISTS idx_workflow_approvals_chat ON workflow_approvals(chat_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_approvals_chat;

-- Remove added columns
ALTER TABLE workflow_approvals DROP COLUMN chat_id;
ALTER TABLE workflow_approvals DROP COLUMN workflow_id;
ALTER TABLE workflow_approvals DROP COLUMN run_id;
ALTER TABLE workflow_approvals DROP COLUMN step_id;
ALTER TABLE workflow_approvals DROP COLUMN title;
ALTER TABLE workflow_approvals DROP COLUMN description;
ALTER TABLE workflow_approvals DROP COLUMN timeout_duration;
ALTER TABLE workflow_approvals DROP COLUMN actions;
ALTER TABLE workflow_approvals DROP COLUMN metadata;
ALTER TABLE workflow_approvals DROP COLUMN resolved_at;
