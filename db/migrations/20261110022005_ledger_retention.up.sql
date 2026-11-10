-- forge:backward-compatible — adds columns with a constant default and new tables; the previous release names its columns on every write to update_stream_counters (the defaults fill the new ones) and never reads any of them.

-- Migration: ledger_retention
--
-- The update ledgers and step history were never pruned (prod reliant-pg,
-- 2026-11-10: chat_updates 10 GB / 7.7M rows, step_executions 8.8 GB / 2.7M
-- rows, zero deletes). The worker's ledger retention (internal/db/
-- ledger_retention.go) now compacts them; these are the two records it keeps.
--
-- update_stream_counters.compacted_through_seq: the chat's compaction
-- watermark. At or below it a chat's ledger keeps only what its readers need
-- (each entity's newest update, see store.CompactChatUpdates), so a replay
-- from a cursor below it would skip rows. A reconnect below it gets a fresh
-- snapshot instead (streaming.chatCursorNeedsSnapshot). It only ever rises, and
-- it is raised BEFORE the rows under it are deleted, so it never under-states
-- what is gone. A user stream's row keeps 0: user_updates are not compacted.
--
-- Adding a column with a constant default is a catalog change; it takes the
-- table's lock for that instant only, and rewrites nothing.
--
-- update_stream_counters.compaction_done_seq: how far compaction has FINISHED
-- deleting. It trails the watermark: a pass resumes from it, so a batch whose
-- delete failed or was interrupted after the watermark rose is done again by
-- the next pass instead of being skipped for good.
ALTER TABLE update_stream_counters
    ADD COLUMN compacted_through_seq bigint NOT NULL DEFAULT 0,
    ADD COLUMN compaction_done_seq bigint NOT NULL DEFAULT 0;

COMMENT ON COLUMN update_stream_counters.compacted_through_seq IS
    'Chat streams: the ledger at or below this sequence is compacted to what snapshots read, so a replay must start at or above it. Only rises.';
COMMENT ON COLUMN update_stream_counters.compaction_done_seq IS
    'Chat streams: compaction has finished deleting through this sequence; the next pass resumes after it. Never above compacted_through_seq.';

-- ledger_retention_cursors: how far a retention pass that walks its table in
-- time order has got, so the next pass starts there instead of re-reading
-- what an earlier one finished. One row per pass (name); the step-history
-- prune is the first ('step_executions': the (completed_at, id) of the last
-- root run it pruned).
CREATE TABLE ledger_retention_cursors (
    name text PRIMARY KEY,
    through_at timestamp with time zone NOT NULL,
    through_id text NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE ledger_retention_cursors IS
    'Where each time-ordered retention pass resumes: everything at or before (through_at, through_id) is done or deferred.';

-- step_retention_deferred_roots: root runs the step prune passed with work
-- still ahead of it — a workflow in the tree that is running, paused, failed
-- (resumable) or finished too recently to prune. The cursor moves on past
-- them; every pass revisits these until the whole tree has been pruned.
CREATE TABLE step_retention_deferred_roots (
    root_id text PRIMARY KEY,
    deferred_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE step_retention_deferred_roots IS
    'Root runs whose step prune is unfinished because part of the tree is not yet prunable; revisited every pass.';
