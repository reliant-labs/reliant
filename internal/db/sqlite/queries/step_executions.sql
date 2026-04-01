-- name: CreateStepExecution :one
INSERT INTO step_executions (
    id, workflow_id, step_id, activity_name, 
    output_json, exit_code, success, duration_ms, 
    loop_node_id, loop_iteration, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetStepExecution :one
SELECT * FROM step_executions WHERE id = ?;

-- name: GetStepExecutions :many
-- Get all executions of a specific step in a workflow (for CEL history queries)
SELECT * FROM step_executions
WHERE workflow_id = ? AND step_id = ?
ORDER BY created_at ASC;

-- name: GetRecentStepExecutions :many
-- Get the N most recent executions of a specific step (for "last N" queries)
SELECT * FROM step_executions
WHERE workflow_id = ? AND step_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: GetAllStepExecutionsForWorkflow :many
-- Get all step executions in a workflow (for full history reconstruction)
SELECT * FROM step_executions
WHERE workflow_id = ?
ORDER BY created_at ASC;

-- name: CountStepExecutions :one
-- Count executions of a specific step (for size() queries)
SELECT COUNT(*) FROM step_executions
WHERE workflow_id = ? AND step_id = ?;

-- name: CountFailedStepExecutions :one
-- Count failed executions of a specific step (for failure counting)
SELECT COUNT(*) FROM step_executions
WHERE workflow_id = ? AND step_id = ? AND (success = 0 OR exit_code != 0);

-- name: GetLastStepExecution :one
-- Get the most recent execution of a specific step (for history[-1] queries)
SELECT * FROM step_executions
WHERE workflow_id = ? AND step_id = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: DeleteStepExecutionsForWorkflow :exec
DELETE FROM step_executions WHERE workflow_id = ?;
