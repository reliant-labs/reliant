-- forge:backward-compatible — no release since v1.8.11 reads through it: the snapshot query walks idx_chat_updates_snapshot_entity_heads (20261029115509), and the approval sweep's update_type = 2 probe is served by that index too.

-- Migration: drop_chat_updates_snapshot_heads_index
--
-- idx_chat_updates_snapshot_heads (chat_id, entity_id, sequence_number DESC)
-- WHERE update_type <> ALL (1, 4, 19) was the chat snapshot's skip-scan index
-- until store.SnapshotHeadsSQL narrowed its predicate to the one
-- idx_chat_updates_snapshot_entity_heads indexes (20261029115509, which said
-- this index is dropped by a later migration: this is it). Prod's last scan
-- of it was 2026-10-31 19:02, the rollout of the release that stopped reading
-- it; its 431k scans all predate that. It indexed every NODE_EXECUTION update
-- (54% of the table) and cost 907MB of cache and a write on most inserts.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_chat_updates_snapshot_heads;
