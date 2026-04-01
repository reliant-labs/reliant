-- +goose Up
-- Add spawned_by_tool_call_id to active_threads to track which tool call created each thread
ALTER TABLE active_threads ADD COLUMN spawned_by_tool_call_id TEXT;

CREATE INDEX idx_active_threads_tool_call ON active_threads(spawned_by_tool_call_id)
    WHERE spawned_by_tool_call_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_active_threads_tool_call;
ALTER TABLE active_threads DROP COLUMN spawned_by_tool_call_id;
