-- +goose Up
-- Step executions table for CEL history queries
-- Stores completed step outputs for workflow history queries like:
-- size(history.filter(exit_code != 0)) >= 3
CREATE TABLE step_executions (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    step_id TEXT NOT NULL,              -- The step ID from workflow definition (e.g., "run_tests", "call_llm")
    activity_name TEXT NOT NULL,        -- The activity type name (e.g., "V2_ExecuteRunStep")
    output_json TEXT,                   -- Full JSON output from the activity
    exit_code INTEGER,                  -- Denormalized for fast queries (nullable for non-run steps)
    success INTEGER,                    -- Boolean: 1 for success, 0 for failure, NULL for unknown
    duration_ms INTEGER,                -- Execution duration in milliseconds
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);

-- Primary index for history queries: lookup by workflow + step
CREATE INDEX idx_step_executions_lookup 
    ON step_executions(workflow_id, step_id, created_at);

-- Index for querying all steps in a workflow (for full history reconstruction)
CREATE INDEX idx_step_executions_workflow
    ON step_executions(workflow_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_step_executions_workflow;
DROP INDEX IF EXISTS idx_step_executions_lookup;
DROP TABLE IF EXISTS step_executions;
