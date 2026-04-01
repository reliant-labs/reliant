-- name: CreateTaskDependency :one
INSERT INTO task_dependencies (
    id, from_task_id, to_task_id, dependency_type, created_at
) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetTaskDependency :one
SELECT * FROM task_dependencies WHERE id = ?;

-- name: ListTaskDependenciesByTask :many
SELECT * FROM task_dependencies
WHERE from_task_id = ? OR to_task_id = ?
ORDER BY created_at ASC;

-- name: ListBlockersForTask :many
SELECT * FROM task_dependencies
WHERE to_task_id = ? AND dependency_type = 1
ORDER BY created_at ASC;

-- name: ListDependenciesByPlan :many
SELECT td.* FROM task_dependencies td
JOIN tasks t ON td.from_task_id = t.id
WHERE t.plan_id = ?
ORDER BY td.created_at ASC;

-- name: DeleteTaskDependency :exec
DELETE FROM task_dependencies WHERE id = ?;

-- name: DeleteTaskDependencyByPair :exec
DELETE FROM task_dependencies
WHERE from_task_id = ? AND to_task_id = ? AND dependency_type = ?;
