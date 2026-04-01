-- name: CreateWorktree :exec
INSERT INTO worktrees (
    id, name, path, branch, base_branch, project_id, chat_id,
    status, is_main, created_at, updated_at, last_active, deleted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorktree :one
SELECT * FROM worktrees WHERE id = ?;

-- name: GetWorktreeByPath :one
SELECT * FROM worktrees WHERE path = ?;

-- name: ListWorktrees :many
SELECT * FROM worktrees
WHERE
    (sqlc.narg('project_id') IS NULL OR project_id = sqlc.narg('project_id'))
    AND (sqlc.narg('chat_id') IS NULL OR chat_id = sqlc.narg('chat_id'))
    AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
    AND deleted_at IS NULL
ORDER BY last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateWorktree :exec
UPDATE worktrees SET
    name = ?,
    branch = ?,
    status = ?,
    base_branch = ?,
    last_active = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: DeleteWorktree :exec
DELETE FROM worktrees WHERE id = ?;

-- name: ArchiveWorktree :exec
UPDATE worktrees SET
    deleted_at = datetime('now', 'utc'),
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: UnarchiveWorktree :exec
UPDATE worktrees SET
    deleted_at = NULL,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: UpdateWorktreeCleanupMetadata :exec
UPDATE worktrees SET
    cleanup_metadata = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;
