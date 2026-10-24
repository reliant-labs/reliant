-- forge:backward-compatible — additive index; previous releases continue using chats unchanged
CREATE INDEX CONCURRENTLY idx_chats_worktree_id ON chats (worktree_id) WHERE worktree_id IS NOT NULL;
