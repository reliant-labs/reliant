-- +goose Up
-- Add temporal_workflow_id to yields table.
-- For inline spawns, the workflow_id is a logical child ID that doesn't correspond
-- to a Temporal workflow execution. The temporal_workflow_id stores the actual parent
-- Temporal workflow ID so that yield signals reach the correct Temporal execution.
-- For root workflows, temporal_workflow_id == workflow_id.

ALTER TABLE yields ADD COLUMN temporal_workflow_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE yields DROP COLUMN temporal_workflow_id;
