-- +goose Up
-- +goose StatementBegin

-- Add forked_from column to track the origin of forked workflows
-- This enables "Reset to Original" functionality for workflows forked from builtins/project files
ALTER TABLE workflow_drafts ADD COLUMN forked_from TEXT;

-- Create index for looking up forks of a particular workflow
CREATE INDEX idx_workflow_drafts_forked_from ON workflow_drafts(forked_from);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_workflow_drafts_forked_from;

-- SQLite doesn't support DROP COLUMN directly, but goose handles this via table recreation
-- For simplicity, we'll leave the column (it's nullable and harmless)

-- +goose StatementEnd
