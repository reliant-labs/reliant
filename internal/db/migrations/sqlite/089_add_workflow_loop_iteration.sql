-- +goose Up
-- Add loop_iteration column to workflows table
-- This allows child workflows spawned within loop iterations to track which iteration they belong to,
-- enabling proper UI grouping of loop iterations that contain both child workflows and inline nodes.
ALTER TABLE workflows ADD COLUMN loop_iteration INTEGER;

CREATE INDEX idx_workflows_loop_iteration ON workflows(parent_id, spawned_by_node_id, loop_iteration) 
    WHERE loop_iteration IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_workflows_loop_iteration;
ALTER TABLE workflows DROP COLUMN loop_iteration;
