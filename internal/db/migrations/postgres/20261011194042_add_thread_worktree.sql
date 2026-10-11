-- +goose Up
-- The workspace a thread works in, when it is not its chat's. A sub-agent
-- spawned with a `worktree` runs its tools in that workspace on the daemon that
-- owns it, while the parent keeps working where it was. NULL (every existing
-- row, and every spawn that names none) means "the chat's worktree", which is
-- what threads always did.
--
-- IF NOT EXISTS: migrations are replayed against databases built from
-- schema.sql (see 20261004201053_add_local_models_to_daemons.sql).
ALTER TABLE threads ADD COLUMN IF NOT EXISTS worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL;
