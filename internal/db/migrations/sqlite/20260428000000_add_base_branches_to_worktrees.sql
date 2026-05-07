-- +goose Up
-- Persist per-repo base branches on worktrees.
--
-- Worktree.BaseBranch (legacy) is one string — fine for single-repo, useless
-- for multi-repo workspaces where repo A may default to `main` and repo B to
-- `master` or `develop`. The CreateWorktreeRequest already accepts a
-- per-repo `base_branches` map, but the resolved values weren't being
-- persisted, so CreatePR couldn't recover them later.
--
-- JSON map<repo_id, base_branch>. Empty/NULL means "fall back to
-- BaseBranch for legacy single-repo or auto-detect at op time."

ALTER TABLE worktrees ADD COLUMN base_branches TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN cleanly; the column will remain unused.
