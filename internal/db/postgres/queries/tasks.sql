-- name: CreateTask :one
INSERT INTO tasks (
    id, plan_id, parent_task_id, title, description, status,
    position, metadata, assignee, created_at, updated_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: ListTasks :many
SELECT * FROM tasks
WHERE plan_id = $1
ORDER BY position ASC;

-- name: UpdateTask :one
UPDATE tasks SET
    title = $1,
    description = $2,
    status = $3,
    position = $4,
    metadata = $5,
    assignee = $6,
    completed_at = $7,
    updated_at = NOW()
WHERE id = $8
RETURNING *;

-- name: DeleteTask :exec
DELETE FROM tasks WHERE id = $1;
