-- +goose NO TRANSACTION
-- +goose Up
-- Index for the chat snapshot's re-keyed read (thread and question updates).
--
-- GetLatestNonMessageUpdatesPerEntity reads the chat's heads in two parts: a
-- skip scan over idx_chat_updates_snapshot_heads for every type that dedups
-- by entity_id, and a separate DISTINCT ON over the types that re-key (THREAD
-- = 3, QUESTION = 18). The skip scan is bounded by the number of entities. The
-- re-keyed read was bounded by nothing useful: no index had update_type in it,
-- so finding one chat's ~758 thread/question rows meant visiting every row
-- idx_chat_updates_snapshot_heads covers for that chat — 165,843 on chat
-- 8bb0a875, 162,919 of them NODE_EXECUTION — and filtering on the heap. That
-- was a Parallel Bitmap Heap Scan of ~10k heap blocks, ~350ms of a ~475ms
-- query, on the read every chat open performs.
--
-- A partial index over exactly the re-keyed types makes the read a single
-- range probe on chat_id that returns only those rows.
--
-- Column order: (chat_id, sequence_number). The DISTINCT ON key is computed
-- from the row (a CASE over entity_id and data::jsonb), so no index order can
-- satisfy its sort; the sort runs over the few hundred rows found either way.
-- What the index must do is find them, and chat_id is the only equality the
-- query has. sequence_number rides along so the index is the natural one for
-- an ordered read of a chat's thread/question history, and it keeps entries
-- small. data is deliberately NOT included: it is an unbounded text payload,
-- and a row over the btree tuple limit would fail the INSERT that writes it.
--
-- The predicate's types are LITERALS and must stay in step with the literal
-- `update_type IN (3, 18)` in snapshotHeadsQuery (internal/db/repo.go): the
-- planner uses a partial index only when it can prove the query's predicate
-- implies the index's, and a prepared statement's generic plan cannot see
-- bound parameters. TestSnapshotHeadsQueryMatchesPartialIndex pins the two.
--
-- Dropped-then-created CONCURRENTLY, as in 20261004010819: a failed
-- CONCURRENTLY build leaves an INVALID index that IF NOT EXISTS would accept
-- as finished, and the planner never uses an invalid index.
DROP INDEX CONCURRENTLY IF EXISTS idx_chat_updates_snapshot_rekeyed;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_snapshot_rekeyed
    ON chat_updates (chat_id, sequence_number)
    WHERE update_type IN (3, 18);
