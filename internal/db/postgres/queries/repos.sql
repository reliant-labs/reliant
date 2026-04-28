-- name: CreateRepo :exec
INSERT INTO repos (
    id, project_id, name, relative_path, remote_url, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetRepo :one
SELECT * FROM repos WHERE id = $1;

-- name: GetRepoByProjectAndPath :one
SELECT * FROM repos WHERE project_id = $1 AND relative_path = $2;

-- name: ListReposByProject :many
SELECT * FROM repos
WHERE project_id = $1
ORDER BY relative_path ASC;

-- name: UpdateRepo :exec
UPDATE repos SET
    name = $2,
    remote_url = $3,
    updated_at = NOW()
WHERE id = $1;

-- name: DeleteRepo :exec
DELETE FROM repos WHERE id = $1;

-- name: DeleteReposByProject :exec
DELETE FROM repos WHERE project_id = $1;
