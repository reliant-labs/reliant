-- forge:backward-compatible — a catalog comment only; no table or column definition changes, and the previous release never reads comments.

-- worktrees.deleted_at is the ARCHIVE stamp, not a soft-delete tombstone: an
-- archived workspace is still a row callers read, update, unarchive and sweep,
-- and deleting one deletes it. Without the marker forge projects it as bun's
-- ,soft_delete, so every query on the table had to say WhereAllWithDeleted and
-- the one hard delete ForceDelete (internal/store/worktree_repo_ext.go).
COMMENT ON COLUMN worktrees.deleted_at IS 'forge:no-soft-delete';
