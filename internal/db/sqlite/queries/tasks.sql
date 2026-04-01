-- name: CreateTask :one
INSERT INTO tasks (
    id, plan_id, parent_task_id, title, description, status,
    position, metadata, assignee, created_at, updated_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- name: ListTasks :many
SELECT * FROM tasks
WHERE plan_id = ?
ORDER BY position ASC;

-- name: UpdateTask :one
UPDATE tasks SET
    title = ?,
    description = ?,
    status = ?,
    position = ?,
    metadata = ?,
    assignee = ?,
    completed_at = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?
RETURNING *;

-- name: DeleteTask :exec
DELETE FROM tasks WHERE id = ?;
