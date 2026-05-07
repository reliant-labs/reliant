-- name: CreateRepo :exec
INSERT INTO repos (
    id, project_id, name, relative_path, remote_url, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetRepo :one
SELECT * FROM repos WHERE id = ?;

-- name: GetRepoByProjectAndPath :one
SELECT * FROM repos WHERE project_id = ? AND relative_path = ?;

-- name: ListReposByProject :many
SELECT * FROM repos
WHERE project_id = ?
ORDER BY relative_path ASC;

-- name: UpdateRepo :exec
UPDATE repos SET
    name = ?,
    remote_url = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: DeleteRepo :exec
DELETE FROM repos WHERE id = ?;

-- name: DeleteReposByProject :exec
DELETE FROM repos WHERE project_id = ?;
