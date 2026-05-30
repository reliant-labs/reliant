-- name: UpsertProjectDaemon :exec
INSERT INTO project_daemons (
    project_id, daemon_id, path, default_branch, cloned_at
) VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (project_id, daemon_id) DO UPDATE SET
    path = EXCLUDED.path,
    default_branch = EXCLUDED.default_branch;

-- name: ListProjectDaemonsForProject :many
SELECT project_id, daemon_id, path, default_branch, cloned_at
FROM project_daemons
WHERE project_id = $1
ORDER BY cloned_at ASC;

-- name: ListProjectDaemonsForDaemon :many
SELECT project_id, daemon_id, path, default_branch, cloned_at
FROM project_daemons
WHERE daemon_id = $1
ORDER BY cloned_at ASC;

-- name: DeleteProjectDaemon :exec
DELETE FROM project_daemons
WHERE project_id = $1 AND daemon_id = $2;
