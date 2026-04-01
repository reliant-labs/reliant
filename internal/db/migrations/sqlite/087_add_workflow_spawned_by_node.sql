-- +goose Up
-- Add spawned_by_node_id to track which workflow node spawned a child workflow
-- This enables better visualization of workflow execution (grouping iterations by step)

ALTER TABLE workflows ADD COLUMN spawned_by_node_id TEXT;

-- Index for efficient lookup of children spawned by a specific node
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_workflows_spawned_by_node;
ALTER TABLE workflows DROP COLUMN spawned_by_node_id;
