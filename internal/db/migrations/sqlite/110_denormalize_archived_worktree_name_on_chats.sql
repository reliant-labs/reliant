-- +goose Up
-- Denormalize worktree name onto chats for archived chat display.
-- This prevents archived chats from showing up under "Unknown Workspace" when
-- the worktree row has been deleted.

ALTER TABLE chats ADD COLUMN archived_worktree_name TEXT;

CREATE INDEX IF NOT EXISTS idx_chats_archived_worktree_name ON chats(archived_worktree_name);

-- +goose Down
DROP INDEX IF EXISTS idx_chats_archived_worktree_name;
ALTER TABLE chats DROP COLUMN archived_worktree_name;