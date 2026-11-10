-- forge:backward-compatible — chat_updates_chat_sequence_key is a unique index on the same (chat_id, sequence_number); every read the previous release makes through this one (MAX(sequence_number), the replay cursor) is served by it, scanned in either direction.

-- Migration: drop_chat_updates_chat_seq_index
--
-- idx_chat_updates_chat_seq (chat_id, sequence_number DESC) duplicates the
-- unique constraint chat_updates_chat_sequence_key (chat_id, sequence_number).
-- A btree scans both directions, and every reader fixes chat_id by equality,
-- so the column's sort direction never changes which index can answer: the
-- planner already used the two interchangeably (prod, 2026-11-10: 645k scans
-- on the key, 30k on this one; MAX(sequence_number) and ChatUpdatesSince plan
-- identically on either). It cost 921MB of a 2.4GB buffer cache and a second
-- index write on every chat update.
--
-- After deploy, check the plans and undo a regression by recreating the index:
-- dev-docs/redundant-index-drop-rollout.md.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_chat_updates_chat_seq;
