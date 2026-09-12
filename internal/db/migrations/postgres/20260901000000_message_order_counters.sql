-- +goose Up
-- Allocate messages.ordinal and messages.seq from transactional counters
-- instead of scanning MAX() at write time.
--
-- What this replaces, and why the scans had to go:
--
--   GetNextOrdinalByThread:  SELECT MAX(ordinal)+1 FROM messages WHERE thread_id = ?
--   GetNextSeqByChat:        a WITH RECURSIVE walk of the whole context-window
--                            fork chain, plus two MAX(seq) lookups.
--
-- Both ran inside SaveMessage's SERIALIZABLE transaction, on EVERY message
-- save. Two problems, one fatal:
--
--  1. A MAX() read followed by an INSERT into the range just read is the
--     textbook SSI anti-pattern. The read takes a predicate lock over a range
--     another writer is about to insert into, so concurrent savers of the same
--     chat abort each other with 40001 "could not serialize access due to
--     read/write dependencies among transactions". Observed in production logs
--     44 times in a single session; 8 of those exhausted all 7 attempts and
--     surfaced to the user as a red error in the transcript.
--
--  2. The recursive CTE re-derived the fork chain on every save, even though a
--     fork chain changes exactly ONCE — when the fork is created. That is the
--     most expensive statement in the transaction, and it was paid per message
--     while holding serializable locks, widening the window in which any other
--     writer could collide.
--
-- A counter row turns both into an O(1) upsert. The row lock still serializes
-- allocation (that is the point: it is what keeps the numbering gap-free and
-- keeps sequence order consistent with commit order), but it is now held for
-- microseconds inside a single statement rather than across five-plus network
-- round trips.
--
-- SCOPES. These mirror the allocators they replace, and the difference matters:
--   ordinal is per-THREAD  (GetNextOrdinalByThread keyed on thread_id)
--   seq     is per-CHAT    (chat-global total order; see
--                           20260802000000_add_message_seq.sql)
-- Using one table with a kind discriminator keeps the allocator generic and
-- matches update_stream_counters, which already works this way.
CREATE TABLE message_order_counters (
    counter_kind text NOT NULL,
    scope_id text NOT NULL,
    last_assigned bigint NOT NULL,
    CONSTRAINT message_order_counters_pkey
        PRIMARY KEY (counter_kind, scope_id),
    CONSTRAINT message_order_counters_kind_check
        CHECK (counter_kind IN ('ordinal', 'seq'))
);

-- Seed ordinal counters from each thread's current high-water mark.
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'ordinal', thread_id, MAX(ordinal)
FROM messages
GROUP BY thread_id;

-- Seed seq counters from each chat's own rows.
--
-- This half is straightforward: a chat's seq high-water mark is the max over
-- the rows carrying its chat_id.
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'seq', chat_id, MAX(seq)
FROM messages
GROUP BY chat_id;

-- Now raise each chat's seq counter to cover INHERITED history.
--
-- This is the subtle half, and getting it wrong is a visible product bug.
--
-- A branched chat DISPLAYS its parent's messages but does not OWN those rows —
-- they keep the parent's chat_id and are resolved on read by walking the
-- context-window chain. So the seed above, which only sees a chat's own rows,
-- restarts a fresh branch at 0. Every message the user then sends lands
-- numerically BENEATH the hundreds of inherited messages rendered above it:
-- the reply is saved and streamed correctly but appears at the top of the
-- transcript, which reads to the user as "my message never arrived".
--
-- That is precisely the defect GetNextSeqByChat's recursive walk existed to
-- prevent, so the counter has to inherit the same high-water mark the walk
-- computed. We do that walk ONCE, here, instead of on every future save.
WITH RECURSIVE chain AS (
    -- Each thread's own context windows, tagged with the thread's chat.
    SELECT cw.id, cw.parent_context_window_id, t.chat_id
    FROM context_windows cw
    JOIN threads t ON t.id = cw.thread_id
    UNION
    -- Walk to parent windows, carrying the ORIGINATING chat_id down so the
    -- inherited max is attributed to the chat that displays it.
    SELECT parent.id, parent.parent_context_window_id, chain.chat_id
    FROM context_windows parent
    JOIN chain ON chain.parent_context_window_id = parent.id
),
inherited AS (
    SELECT chain.chat_id, MAX(m.seq) AS max_seq
    FROM chain
    JOIN messages m ON m.context_window_id = chain.id
    GROUP BY chain.chat_id
)
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'seq', inherited.chat_id, inherited.max_seq
FROM inherited
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(
        message_order_counters.last_assigned,
        EXCLUDED.last_assigned
    );

-- +goose Down
DROP TABLE message_order_counters;
