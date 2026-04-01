-- +goose Up
-- Add entity_id column to workflow_events for efficient event querying by associated entity
-- Example: entity_id = message_id for tool-related events, allowing fast lookup of all events for a message

ALTER TABLE workflow_events ADD COLUMN entity_id TEXT;

-- Index for fast lookups by entity
CREATE INDEX idx_workflow_events_entity ON workflow_events(workflow_id, event_name, entity_id) WHERE entity_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_events_entity;
ALTER TABLE workflow_events DROP COLUMN entity_id;
