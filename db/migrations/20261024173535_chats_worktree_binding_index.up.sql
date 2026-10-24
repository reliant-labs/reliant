-- forge:backward-compatible — additive index; previous releases continue using chats unchanged
--
-- Re-versioned 20261024024221_chats_worktree_binding_index. That version sorted
-- below 20261024035148, which prod applied first (from a release branch), so
-- the migrator refused it as never-applicable and every api-server on main
-- crash-looped. IF NOT EXISTS keeps this a no-op where 024221 already ran
-- (dev, e2e); the migrator ignores that database's leftover 024221 row, since
-- it only checks versions this binary embeds.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chats_worktree_id ON chats (worktree_id) WHERE worktree_id IS NOT NULL;
