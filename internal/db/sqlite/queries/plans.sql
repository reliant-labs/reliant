-- name: CreatePlan :one
INSERT INTO plans (
    id, thread_id, title, description, status, complexity, project_id,
    created_at, updated_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPlan :one
SELECT * FROM plans WHERE id = ?;

-- name: ListPlans :many
SELECT * FROM plans
WHERE thread_id = ?
ORDER BY created_at DESC;

-- name: ListPlansByChatID :many
SELECT plans.* FROM plans
JOIN threads ON threads.id = plans.thread_id
WHERE threads.conversation_id = ?
ORDER BY plans.created_at DESC;

-- name: ListPlansByProject :many
SELECT * FROM plans
WHERE project_id = ?
ORDER BY created_at DESC;

-- name: UpdatePlan :one
UPDATE plans SET
    title = ?,
    description = ?,
    status = ?,
    complexity = ?,
    completed_at = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?
RETURNING *;

-- name: DeletePlan :exec
DELETE FROM plans WHERE id = ?;
