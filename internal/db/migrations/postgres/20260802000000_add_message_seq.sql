-- +goose Up
-- This migration adds `messages.seq`: a real per-CHAT total order, backfilled
-- once here and populated by every writer from this point on.
--
-- The defect it replaces: `ordinal` is allocated per-THREAD
-- (GetNextOrdinalByThread: MAX(ordinal)+1 WHERE thread_id = $1), but the
-- primary read path, ListMessages, sorts a whole CHAT by it
-- ("WHERE chat_id = $1 ORDER BY ordinal ASC"). Two messages in different
-- threads of the same chat can carry the same ordinal, or a later message in
-- thread B can carry a smaller ordinal than an earlier message in thread A —
-- the column simply does not encode chat-wide order. ListRecentMessages says
-- as much in its own comment. The frontend's only recourse
-- (web/src/lib/messageOrder.ts) is to reconstruct order at read time by
-- interleaving threads on created_at, with each thread's timestamps clamped
-- to be non-decreasing in ordinal order first (because a thread's own
-- timestamps are not trustworthy either — see below). `seq` computes that
-- same reconstruction once, here, and freezes it as a real column so no
-- reader has to re-derive it.
--
-- Backfill rule — same algorithm as messageOrder.ts, run server-side:
--   1. Within each thread, order rows by ordinal (the thread's own
--      canonical order is never in question — only cross-thread order is
--      unknown). Compute a running max of created_at over that order: this
--      is the "clamped" timestamp. It exists because repair messages,
--      attachment messages, and client-built placeholders have shipped with
--      wrong timestamps (see cleanup.go / db_helpers.go comments on UTC
--      handling), so a raw created_at sort can invert a thread against its
--      own ordinal order. The clamp makes that structurally impossible: it
--      is non-decreasing by construction, so ordering by it can never place
--      a later-ordinal row before an earlier-ordinal row of the same
--      thread.
--   2. Interleave every thread in the chat on (clamped_time, thread_id,
--      ordinal, id) — the same tie-break chain messageOrder.ts uses
--      (time, then thread key, then in-thread position).
--   3. Assign seq densely per chat via ROW_NUMBER() - 1 over that order.
--
-- This backfill is only correct because of
-- 20260728000000_timestamps_to_timestamptz.sql. Before that migration,
-- `created_at` was written as local wall-clock time by some paths (e.g. a
-- bare time.Now()) and as UTC by others into the very same `timestamp
-- without time zone` column, so comparing two created_at values from
-- different code paths was comparing values offset from each other by the
-- writing host's timezone — a cross-thread interleave on such a column
-- would be wrong by that offset. With timestamptz, every stored value is a
-- comparable instant regardless of which writer produced it, which is the
-- precondition this whole backfill rests on.
--
-- seq is additive, not a replacement: ordinal keeps being written (and
-- keeps being the source of within-thread order); no read path changes in
-- this migration.

ALTER TABLE messages ADD COLUMN seq bigint;

-- +goose StatementBegin
DO $$
BEGIN
    -- Clamped-timestamp CTE: one row per message, with clamp_ts = the
    -- running max of created_at within its thread, ordered by ordinal (ties
    -- broken by id for determinism — two messages can share an ordinal only
    -- if they predate the messages_thread_ordinal_key backfill in the prior
    -- migration, but the window function still needs a deterministic order
    -- over them).
    WITH clamped AS (
        SELECT
            id,
            chat_id,
            thread_id,
            ordinal,
            MAX(created_at) OVER (
                PARTITION BY thread_id
                ORDER BY ordinal, id
                ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
            ) AS clamp_ts
        FROM messages
    ),
    ordered AS (
        SELECT
            id,
            chat_id,
            ROW_NUMBER() OVER (
                PARTITION BY chat_id
                ORDER BY clamp_ts, thread_id, ordinal, id
            ) - 1 AS new_seq
        FROM clamped
    )
    UPDATE messages m
    SET seq = o.new_seq
    FROM ordered o
    WHERE m.id = o.id;

    -- Sanity check: every row must have been assigned a seq. A NULL here
    -- would mean a message's thread_id didn't round-trip through the CTE
    -- above, which would indicate a data integrity problem the FK
    -- constraints in 20260801000000_conversation_integrity_constraints.sql
    -- were supposed to rule out already.
    IF EXISTS (SELECT 1 FROM messages WHERE seq IS NULL) THEN
        RAISE EXCEPTION 'messages.seq backfill left NULL rows';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE messages ALTER COLUMN seq SET NOT NULL;

-- Same rationale as messages_thread_ordinal_key in the prior migration: make
-- a seq collision fail loudly at write time instead of silently reordering
-- a chat at read time.
ALTER TABLE messages ADD CONSTRAINT messages_chat_seq_key UNIQUE (chat_id, seq);

-- +goose Down
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_chat_seq_key;
ALTER TABLE messages DROP COLUMN IF EXISTS seq;
