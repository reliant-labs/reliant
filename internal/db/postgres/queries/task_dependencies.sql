-- name: CreateTaskDependency :one
INSERT INTO task_dependencies (
    id, from_task_id, to_task_id, dependency_type, created_at
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetTaskDependency :one
SELECT * FROM task_dependencies WHERE id = $1;

-- name: ListTaskDependenciesByTask :many
SELECT * FROM task_dependencies
WHERE from_task_id = $1 OR to_task_id = $1
ORDER BY created_at ASC;

-- name: ListBlockersForTask :many
SELECT * FROM task_dependencies
WHERE to_task_id = $1 AND dependency_type = 1
ORDER BY created_at ASC;

-- name: ListDependenciesByPlan :many
SELECT td.* FROM task_dependencies td
JOIN tasks t ON td.from_task_id = t.id
WHERE t.plan_id = $1
ORDER BY td.created_at ASC;

-- name: DeleteTaskDependency :exec
DELETE FROM task_dependencies WHERE id = $1;

-- name: DeleteTaskDependencyByPair :exec
DELETE FROM task_dependencies
WHERE from_task_id = $1 AND to_task_id = $2 AND dependency_type = $3;
