-- +goose Up
-- Add workflow_id and run_id to workflow_events so we can signal the correct workflow instance
ALTER TABLE workflow_events ADD COLUMN workflow_id TEXT;
ALTER TABLE workflow_events ADD COLUMN run_id TEXT;

-- Create index for looking up by workflow_id
CREATE INDEX idx_workflow_events_workflow_id ON workflow_events(workflow_id);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_events_workflow_id;
ALTER TABLE workflow_events DROP COLUMN run_id;
ALTER TABLE workflow_events DROP COLUMN workflow_id;
