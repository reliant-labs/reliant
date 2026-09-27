-- name: CreateProject :exec
INSERT INTO projects (
    id, user_id, name, path, description, is_git_repo, default_branch, remote_url, is_forge,
    forge_project_name, created_at, updated_at, last_active
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1;

-- name: GetProjectByPath :one
SELECT * FROM projects WHERE path = $1;

-- name: GetProjectByPathAndUser :one
SELECT * FROM projects WHERE path = $1 AND user_id = $2;

-- name: GetProjectByRemoteURLAndUser :one
SELECT * FROM projects WHERE remote_url = $1 AND user_id = $2;

-- name: GetProjectWithUserCheck :one
SELECT * FROM projects WHERE id = $1 AND user_id = $2;

-- name: ListProjects :many
SELECT * FROM projects
WHERE user_id = sqlc.arg('user_id')
ORDER BY last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateProject :exec
UPDATE projects SET
    name = $1,
    description = $2,
    is_git_repo = $3,
    default_branch = $4,
    remote_url = $5,
    is_forge = $6,
    forge_project_name = $7,
    last_active = $8,
    updated_at = NOW()
WHERE id = $9 AND user_id = $10;

-- name: SetProjectForgeName :execrows
-- Records forge's name for a project (forge.yaml `name`) and marks it a forge
-- project. A no-op — zero rows, no updated_at churn — when both already hold,
-- so callers can run it on every successful forge read.
UPDATE projects SET
    is_forge = TRUE,
    forge_project_name = sqlc.arg('forge_project_name')::text,
    updated_at = NOW()
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('user_id')
  AND (NOT is_forge OR forge_project_name IS DISTINCT FROM sqlc.arg('forge_project_name')::text);

-- name: TouchProject :exec
UPDATE projects SET
    last_active = NOW(),
    updated_at = NOW()
WHERE id = $1 AND user_id = $2;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1 AND user_id = $2;
