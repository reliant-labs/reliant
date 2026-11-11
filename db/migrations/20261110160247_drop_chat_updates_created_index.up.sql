-- forge:backward-compatible — no release queries chat_updates by created_at alone; the one created_at read (store.ErrorUpdatesInRangeSQL) goes through idx_chat_updates_errors_created.

-- Migration: drop_chat_updates_created_index
--
-- idx_chat_updates_created (created_at DESC) has no reader in the product:
-- every chat_updates read fixes chat_id, and the only one ranged on created_at
-- (the errors of a history page) has its own partial index,
-- idx_chat_updates_errors_created (chat_id, created_at) WHERE update_type = 6.
-- Prod counted 133 scans of it in 69 days (ad-hoc operator queries), against
-- 314MB of cache and an index write on every chat update.
--
-- After deploy, check the plans and undo a regression by recreating the index:
-- dev-docs/redundant-index-drop-rollout.md.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_chat_updates_created;
