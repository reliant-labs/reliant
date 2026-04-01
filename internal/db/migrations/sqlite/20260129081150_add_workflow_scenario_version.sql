-- +goose Up
-- Add version column for optimistic concurrency control on workflow_scenarios
-- Using integer version avoids timestamp precision issues with RFC3339

ALTER TABLE workflow_scenarios ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE workflow_scenarios DROP COLUMN version;
