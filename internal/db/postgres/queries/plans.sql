-- name: CreatePlan :one
INSERT INTO plans (
    id, thread_id, title, description, status, complexity, project_id,
    created_at, updated_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1;

-- name: ListPlans :many
SELECT * FROM plans
WHERE thread_id = $1
ORDER BY created_at DESC;

-- name: ListPlansByChatID :many
SELECT plans.* FROM plans
JOIN threads ON threads.id = plans.thread_id
WHERE threads.conversation_id = $1
ORDER BY plans.created_at DESC;

-- name: ListPlansByProject :many
SELECT * FROM plans
WHERE project_id = $1
ORDER BY created_at DESC;

-- name: UpdatePlan :one
UPDATE plans SET
    title = $1,
    description = $2,
    status = $3,
    complexity = $4,
    completed_at = $5,
    updated_at = NOW()
WHERE id = $6
RETURNING *;

-- name: DeletePlan :exec
DELETE FROM plans WHERE id = $1;
