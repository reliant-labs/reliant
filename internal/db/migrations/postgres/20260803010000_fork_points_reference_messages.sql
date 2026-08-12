-- +goose Up
-- Fork points stop being integer offsets and become foreign keys to the
-- message they actually mean.
--
-- What was here: threads.fork_at_ordinal + threads.fork_at_context_window_id,
-- and context_windows.fork_at_ordinal. A fork point was expressed as "position
-- N inside that context window", and resolution
-- (threads/messages.go resolveMessagesFromCW) filtered the parent's messages
-- with `msg.Ordinal <= forkOrdinal` gated on
-- `msg.ContextWindowID == parentCWID`.
--
-- Two problems with an offset:
--
-- 1. It is unverifiable. Nothing stops fork_at_ordinal from naming a position
--    no message occupies, and nothing notices if the message it named is
--    deleted -- the fork silently starts inheriting a different amount of
--    history. A foreign key is checkable, and ON DELETE RESTRICT makes the
--    failure loud at the moment of deletion instead of silent at the next read.
--
-- 2. `ordinal` is on its way out. 20260802000000_add_message_seq.sql moved
--    every read path to the chat-global `messages.seq`; these fork comparisons
--    are the last consumers of the per-thread ordinal, so they are what pins
--    the column in place.
--
-- Why one column replaces two on threads: fork_at_context_window_id existed
-- only to disambiguate which context window the ordinal was an offset into.
-- A message id needs no disambiguation -- it already knows its own context
-- window (messages.context_window_id) and thread. Resolution reads the fork
-- message and takes its context_window_id where the fork CW was needed before.
--
-- ---------------------------------------------------------------------------
-- Backfill
-- ---------------------------------------------------------------------------
-- The fork ordinal refers to a message in the PARENT context window, so the
-- target is resolved by (parent context window, ordinal):
--   * threads:         fork_at_context_window_id  + fork_at_ordinal
--   * context_windows: parent_context_window_id   + fork_at_ordinal
--
-- Exact ordinal match, not "greatest ordinal <= N". The fork ordinal is
-- produced by copying an existing message's ordinal
-- (threads/service.go resolveForkPoint reads msg.Ordinal), so a row whose
-- ordinal does not exist in that context window is a dangling reference, not
-- a value to round down. Rounding down would silently invent a fork point one
-- message earlier than the data claims; leaving NULL says "this fork point
-- could not be resolved" honestly. Both are checked and reported by the
-- verification block below.
--
-- On the dataset this was developed against: 154 of 154 threads and 154 of 154
-- context_windows resolved to an exact-ordinal message in the correct parent
-- context window, and all 154 resolved thread fork messages belong to the
-- named parent_thread_id.
--
-- Rows that do not resolve keep NULL. A NULL fork_at_message_id on a context
-- window with a parent means "inherit the parent's messages unfiltered", which
-- is the same thing the old code did when fork_at_ordinal was NULL, so an
-- unresolvable fork point degrades to the most inclusive reading rather than
-- silently dropping history.

ALTER TABLE threads ADD COLUMN fork_at_message_id text;
ALTER TABLE context_windows ADD COLUMN fork_at_message_id text;

UPDATE threads t
SET fork_at_message_id = m.id
FROM messages m
WHERE t.fork_at_ordinal IS NOT NULL
  AND m.context_window_id = t.fork_at_context_window_id
  AND m.ordinal = t.fork_at_ordinal;

UPDATE context_windows cw
SET fork_at_message_id = m.id
FROM messages m
WHERE cw.fork_at_ordinal IS NOT NULL
  AND m.context_window_id = cw.parent_context_window_id
  AND m.ordinal = cw.fork_at_ordinal;

-- +goose StatementBegin
DO $$
DECLARE
    t_total    bigint;
    t_resolved bigint;
    cw_total    bigint;
    cw_resolved bigint;
BEGIN
    SELECT count(*) FILTER (WHERE fork_at_ordinal IS NOT NULL),
           count(*) FILTER (WHERE fork_at_message_id IS NOT NULL)
      INTO t_total, t_resolved FROM threads;

    SELECT count(*) FILTER (WHERE fork_at_ordinal IS NOT NULL),
           count(*) FILTER (WHERE fork_at_message_id IS NOT NULL)
      INTO cw_total, cw_resolved FROM context_windows;

    -- Reported, not enforced: an unresolvable fork point is a pre-existing
    -- dangling reference in the data, and refusing to migrate would leave the
    -- install stuck on a schema whose whole point is that such references
    -- become impossible from here on.
    RAISE NOTICE 'fork point backfill: threads %/% resolved, context_windows %/% resolved',
        t_resolved, t_total, cw_resolved, cw_total;
END $$;
-- +goose StatementEnd

-- ON DELETE RESTRICT, matching messages_thread_id_fkey in
-- 20260801000000_conversation_integrity_constraints.sql and for the same
-- reason: deleting a message that a fork depends on would silently change
-- which history that fork inherits, so the delete should fail loudly instead.
-- (Temporal activity retries do delete messages -- see DeleteMessage -- but
-- only messages they just wrote, which no fork can point at yet.)
ALTER TABLE threads
    ADD CONSTRAINT threads_fork_at_message_id_fkey
    FOREIGN KEY (fork_at_message_id) REFERENCES messages(id) ON DELETE RESTRICT;

ALTER TABLE context_windows
    ADD CONSTRAINT context_windows_fork_at_message_id_fkey
    FOREIGN KEY (fork_at_message_id) REFERENCES messages(id) ON DELETE RESTRICT;

ALTER TABLE threads DROP COLUMN fork_at_ordinal;
ALTER TABLE threads DROP COLUMN fork_at_context_window_id;
ALTER TABLE context_windows DROP COLUMN fork_at_ordinal;

-- +goose Down
-- Rebuilds the offsets from the message references. This is lossy in exactly
-- one way: a fork point that failed to resolve on the way up is NULL here and
-- cannot be recovered, because the ordinal it named is gone with the column.
ALTER TABLE threads ADD COLUMN fork_at_ordinal bigint;
ALTER TABLE threads ADD COLUMN fork_at_context_window_id text;
ALTER TABLE context_windows ADD COLUMN fork_at_ordinal bigint;

UPDATE threads t
SET fork_at_ordinal = m.ordinal,
    fork_at_context_window_id = m.context_window_id
FROM messages m
WHERE t.fork_at_message_id = m.id;

UPDATE context_windows cw
SET fork_at_ordinal = m.ordinal
FROM messages m
WHERE cw.fork_at_message_id = m.id;

ALTER TABLE threads DROP CONSTRAINT IF EXISTS threads_fork_at_message_id_fkey;
ALTER TABLE context_windows DROP CONSTRAINT IF EXISTS context_windows_fork_at_message_id_fkey;
ALTER TABLE threads DROP COLUMN IF EXISTS fork_at_message_id;
ALTER TABLE context_windows DROP COLUMN IF EXISTS fork_at_message_id;
