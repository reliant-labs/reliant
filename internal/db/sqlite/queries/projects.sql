-- name: CreateProject :exec
INSERT INTO projects (
    id, user_id, name, path, description, is_git_repo, default_branch,
    created_at, updated_at, last_active
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: GetProjectByPath :one
SELECT * FROM projects WHERE path = ?;

-- name: GetProjectByPathAndUser :one
SELECT * FROM projects WHERE path = ? AND user_id = ?;

-- name: GetProjectWithUserCheck :one
SELECT * FROM projects WHERE id = ? AND user_id = ?;

-- name: ListProjects :many
SELECT * FROM projects
WHERE user_id = sqlc.arg('user_id')
ORDER BY last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateProject :exec
UPDATE projects SET
    name = ?,
    description = ?,
    is_git_repo = ?,
    default_branch = ?,
    last_active = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ? AND user_id = ?;

-- name: TouchProject :exec
UPDATE projects SET
    last_active = datetime('now', 'utc'),
    updated_at = datetime('now', 'utc')
WHERE id = ? AND user_id = ?;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ? AND user_id = ?;