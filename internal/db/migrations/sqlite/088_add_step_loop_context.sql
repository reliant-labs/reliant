-- +goose Up
-- Add loop context to step executions so UI can group steps by loop iteration
ALTER TABLE step_executions ADD COLUMN loop_node_id TEXT;
ALTER TABLE step_executions ADD COLUMN loop_iteration INTEGER;

CREATE INDEX idx_step_executions_loop 
    ON step_executions(workflow_id, loop_node_id, loop_iteration) 
    WHERE loop_node_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_step_executions_loop;
ALTER TABLE step_executions DROP COLUMN loop_iteration;
ALTER TABLE step_executions DROP COLUMN loop_node_id;
