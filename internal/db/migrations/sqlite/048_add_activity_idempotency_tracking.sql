-- +goose Up
-- Migration: Add activity ID tracking for idempotent retries
-- Purpose: Associate all content/state with Temporal activity IDs to enable cleanup on retry

-- Add activity tracking columns to message_content_blocks
ALTER TABLE message_content_blocks ADD COLUMN activity_id TEXT;
ALTER TABLE message_content_blocks ADD COLUMN workflow_run_id TEXT;
ALTER TABLE message_content_blocks ADD COLUMN attempt_number INTEGER DEFAULT 1;

-- Add activity tracking columns to tool_calls
ALTER TABLE tool_calls ADD COLUMN activity_id TEXT;
ALTER TABLE tool_calls ADD COLUMN workflow_run_id TEXT;

-- Add indexes for cleanup queries
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id, attempt_number)
WHERE activity_id IS NOT NULL;

CREATE INDEX idx_tool_calls_activity ON tool_calls(activity_id)
WHERE activity_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_tool_calls_activity;
DROP INDEX IF EXISTS idx_content_blocks_activity;

-- Note: SQLite doesn't support DROP COLUMN directly in older versions
-- For now, columns will remain but be unused during rollback
-- If full rollback is needed, table recreation would be required
