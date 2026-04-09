-- +goose Up
-- Add temporal_workflow_id to approvals table.
-- For signal-based approval resolution, we need to know which Temporal workflow
-- execution to signal. For inline spawns, the workflow_id is a logical child ID
-- that doesn't correspond to a Temporal workflow execution. The temporal_workflow_id
-- stores the actual parent Temporal workflow ID so that approval signals reach
-- the correct Temporal execution.

ALTER TABLE approvals ADD COLUMN temporal_workflow_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE approvals DROP COLUMN temporal_workflow_id;
