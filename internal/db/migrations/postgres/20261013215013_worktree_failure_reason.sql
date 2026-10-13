-- +goose Up

-- Why a worktree's creation failed, in words the user can act on ("base
-- branch 'x' doesn't exist in reliant — check the name ..."). Creation from
-- the UI is asynchronous: the row is inserted CREATING and settled to FAILED
-- in the background, so this column is the only way the reason reaches the
-- person who asked. Written only with a FAILED status (internal/workspacecreate);
-- empty for every other row and for a server fault, whose detail stays in the
-- server's logs.
--
-- Re-runnable (see 20261005170614).
ALTER TABLE worktrees ADD COLUMN IF NOT EXISTS failure_reason text NOT NULL DEFAULT '';
