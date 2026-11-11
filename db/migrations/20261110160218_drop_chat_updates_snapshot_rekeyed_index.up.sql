-- forge:backward-compatible — no release since v1.8.15 reads through it: the snapshot's re-keyed half walks idx_chat_updates_snapshot_rekey_heads (20261101165219).

-- Migration: drop_chat_updates_snapshot_rekeyed_index
--
-- idx_chat_updates_snapshot_rekeyed (chat_id, sequence_number) WHERE
-- update_type IN (3, 18) served the snapshot's THREAD/QUESTION read until it
-- became a skip scan over idx_chat_updates_snapshot_rekey_heads, which covers
-- the same rows (20261101165219, which said this index is dropped by a later
-- migration: this is it). Prod's last scan of it was 2026-11-02 01:28.
--
-- After deploy, check the plans and undo a regression by recreating the index:
-- dev-docs/redundant-index-drop-rollout.md.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_chat_updates_snapshot_rekeyed;
