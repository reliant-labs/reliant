-- name: CreateWorktree :exec
INSERT INTO worktrees (
    id, name, path, branch, base_branch, project_id, chat_id,
    status, is_main, created_at, updated_at, last_active, deleted_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetWorktree :one
SELECT * FROM worktrees WHERE id = $1;

-- name: GetWorktreeByPath :one
SELECT * FROM worktrees WHERE path = $1;

-- name: ListWorktrees :many
SELECT * FROM worktrees
WHERE
    (sqlc.narg('project_id')::text IS NULL OR project_id = sqlc.narg('project_id')::text)
    AND (sqlc.narg('chat_id')::text IS NULL OR chat_id = sqlc.narg('chat_id')::text)
    AND (sqlc.narg('status')::integer IS NULL OR status = sqlc.narg('status')::integer)
    AND (sqlc.arg('include_archived')::boolean OR deleted_at IS NULL)
ORDER BY (deleted_at IS NOT NULL), last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateWorktree :exec
UPDATE worktrees SET
    name = $1,
    branch = $2,
    status = $3,
    base_branch = $4,
    last_active = $5,
    updated_at = NOW()
WHERE id = $6;

-- name: DeleteWorktree :exec
DELETE FROM worktrees WHERE id = $1;

-- name: ArchiveWorktree :exec
UPDATE worktrees SET
    deleted_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: UnarchiveWorktree :exec
UPDATE worktrees SET
    deleted_at = NULL,
    updated_at = NOW()
WHERE id = $1;

-- name: UpdateWorktreeCleanupMetadata :exec
UPDATE worktrees SET
    cleanup_metadata = $1,
    updated_at = NOW()
WHERE id = $2;