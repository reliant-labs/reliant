-- name: CreateWorktree :exec
INSERT INTO worktrees (
    id, name, path, branch, base_branch, project_id, chat_id,
    status, is_main, created_at, updated_at, last_active, deleted_at,
    base_branches, daemon_id, idempotency_key
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

-- name: GetWorktreeByIdempotencyKey :one
-- Scoped by project because the key is client-generated; two projects must be
-- able to use the same key without colliding.
SELECT * FROM worktrees
WHERE project_id = $1 AND idempotency_key = $2;

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
-- `path` is settable because asynchronous creation only learns the on-disk
-- location after the daemon reports it: the row is inserted as CREATING with
-- an empty path, then settled here.
UPDATE worktrees SET
    name = $1,
    branch = $2,
    status = $3,
    base_branch = $4,
    base_branches = $5,
    last_active = $6,
    path = $7,
    updated_at = NOW()
WHERE id = $8;

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

-- name: ListWorktreesForReclaim :many
-- Every workspace whose directory a daemon may still hold: not main, has a
-- path, and not already known to be removed. Status 5 is CREATING and 6 is
-- FAILED (WorktreeStatus in worktree.proto); neither has a directory yet. Archived rows are the ones to remove; the rest are locked as
-- reliant-owned. owner_user_id lets the sweep find the daemon's connection.
-- cleanup_metadata is only ever NULL or JSON we wrote, but the cast sits inside
-- a CASE so a hand-edited value cannot fail the whole listing.
SELECT
    sqlc.embed(w),
    p.user_id AS owner_user_id,
    p.path AS project_path
FROM worktrees w
JOIN projects p ON p.id = w.project_id
WHERE w.is_main = false
  AND w.path <> ''
  AND w.status NOT IN (5, 6)
  AND (CASE WHEN w.cleanup_metadata IS NULL OR w.cleanup_metadata = '' THEN false
            ELSE COALESCE((w.cleanup_metadata::jsonb ->> 'directory_deleted')::boolean, false) END) = false
ORDER BY w.last_active DESC;

-- name: ListHeldWorktreesForUser :many
-- Archived workspaces the daemon declined to remove on its own, for the
-- storage inbox item.
SELECT
    sqlc.embed(w),
    p.name AS project_name,
    p.path AS project_path
FROM worktrees w
JOIN projects p ON p.id = w.project_id
WHERE p.user_id = $1
  AND w.is_main = false
  AND w.deleted_at IS NOT NULL
  AND (CASE WHEN w.cleanup_metadata IS NULL OR w.cleanup_metadata = '' THEN ''
            ELSE COALESCE(w.cleanup_metadata::jsonb ->> 'held_reason', '') END) <> ''
  AND (CASE WHEN w.cleanup_metadata IS NULL OR w.cleanup_metadata = '' THEN false
            ELSE COALESCE((w.cleanup_metadata::jsonb ->> 'directory_deleted')::boolean, false) END) = false
ORDER BY w.deleted_at;

-- name: AdoptWorktreeDaemon :exec
-- A daemon that found a workspace's directory on its disk has proven it owns
-- it. Rows created before daemon_id was recorded get it filled in here, once.
UPDATE worktrees SET daemon_id = $1 WHERE id = $2 AND daemon_id IS NULL;


-- name: ListLiveWorktreePathsForUser :many
-- Every path one user's unarchived worktree rows claim. An archived row's
-- directory may only be removed when none of the SAME user's live rows has a
-- path that equals, contains or sits inside it. Scoped by user because another
-- tenant's rows say nothing about this user's directories.
SELECT w.id, w.path FROM worktrees w
JOIN projects p ON p.id = w.project_id
WHERE p.user_id = $1 AND w.deleted_at IS NULL AND w.path <> '';

-- name: GetLiveWorktreeByName :one
-- The one unarchived row holding a name; worktrees_project_id_name_live_key
-- guarantees there is at most one.
SELECT * FROM worktrees
WHERE project_id = $1 AND name = $2 AND deleted_at IS NULL;

-- name: ListWorktreesByBranch :many
-- Every row, archived included, recorded against a branch: an archived row's
-- checkout or branch may outlive the archive.
SELECT * FROM worktrees
WHERE project_id = $1 AND branch = $2;
