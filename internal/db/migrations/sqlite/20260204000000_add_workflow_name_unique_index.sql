-- +goose Up
-- Add unique index on (user_id, lower(name)) to prevent duplicate workflow names
-- This is a safety net in addition to application-level checks
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_drafts_user_name_unique 
ON workflow_drafts (user_id, name COLLATE NOCASE);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_drafts_user_name_unique;
