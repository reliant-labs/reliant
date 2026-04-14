-- name: CreateYield :exec
-- Create a new yield record
INSERT INTO yields (
    id, chat_id, workflow_id, thread_id, step_id, loop_node_id, loop_iteration,
    status, action_taken, metadata, created_at, resolved_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetYieldByID :one
-- Get a specific yield by ID
SELECT * FROM yields WHERE id = ?;

-- name: GetPendingYieldByChatID :one
-- Get the most recent pending yield for a chat
SELECT * FROM yields
WHERE chat_id = ? AND status = 1
ORDER BY created_at DESC
LIMIT 1;

-- name: GetPendingYieldsByWorkflow :many
-- Get all pending yields for a workflow
SELECT * FROM yields
WHERE workflow_id = ? AND status = 1;

-- name: ResolveYield :exec
-- Resolve a yield with an action
UPDATE yields
SET status = 2, action_taken = ?, resolved_at = CURRENT_TIMESTAMP
WHERE id = ?;