-- +goose Up
-- Add version column for optimistic concurrency control
-- Using integer version avoids timestamp precision issues with RFC3339

ALTER TABLE workflow_drafts ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
-- SQLite doesn't support DROP COLUMN directly, but goose handles this
ALTER TABLE workflow_drafts DROP COLUMN version;
