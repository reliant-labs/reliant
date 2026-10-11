-- +goose Up
-- A worktree name is unique among the project's LIVE rows only. Archiving
-- releases the name: the archived row keeps its history and its directory is
-- settled by the sweep, but nothing looks it up by name any more.
ALTER TABLE worktrees DROP CONSTRAINT IF EXISTS worktrees_project_id_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS worktrees_project_id_name_live_key ON worktrees (project_id, name) WHERE deleted_at IS NULL;
