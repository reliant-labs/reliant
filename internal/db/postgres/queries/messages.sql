-- name: CreateMessage :exec
-- Creates a message in the new schema (uses context_window_id, has display_style)
INSERT INTO messages (
    id, chat_id, ordinal, seq, thread_id, context_window_id,
    node_id, node_path,
    role, display_style, model, agent,
    token_count, cost,
    workflow_id, run_id, activity_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19);

-- name: GetMessage :one
SELECT * FROM messages WHERE id = $1;

-- name: ListMessages :many
SELECT * FROM messages
WHERE chat_id = $1
ORDER BY seq ASC;

-- name: UpdateMessage :exec
UPDATE messages SET
    token_count = $1,
    cost = $2,
    updated_at = NOW()
WHERE id = $3;

-- name: GetMessageByActivityID :one
SELECT * FROM messages
WHERE chat_id = $1 AND activity_id = $2
LIMIT 1;

-- name: CreateMessageIfNotExists :exec
-- ON CONFLICT DO NOTHING for idempotent message creation
INSERT INTO messages (
    id, chat_id, ordinal, seq, thread_id, context_window_id,
    node_id, node_path,
    role, display_style, model, agent,
    token_count, cost,
    workflow_id, run_id, activity_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
ON CONFLICT(id) DO NOTHING;

-- name: GetNextOrdinalByThread :one
-- Get next ordinal for a thread (uses denormalized thread_id)
SELECT COALESCE(MAX(ordinal), -1) + 1 AS next_ordinal
FROM messages
WHERE thread_id = $1;

-- name: GetNextSeqByChat :one
-- Get next seq for a chat (chat-global total order; see
-- 20260802000000_add_message_seq.sql for why this exists alongside ordinal).
--
-- A branched chat DISPLAYS its parent's history but does not own those rows —
-- they still carry the parent chat_id, resolved on read through the context
-- window chain. Allocating from this chat's rows alone therefore restarts a
-- branch at seq 0, and every message the user sends lands numerically beneath
-- the ~900 inherited messages shown above it: the reply is saved and streamed
-- correctly but renders at the top of the transcript, which reads as "my
-- message never arrived".
--
-- The high-water mark has to span the whole context-window chain the branch
-- resolves, not just its own rows. Walking the chain here keeps the allocator
-- honest without teaching every caller about forks.
--
-- The two halves are deliberately SEPARATE max lookups combined with GREATEST,
-- rather than the single `WHERE chat_id = $1 OR context_window_id IN (...)`
-- this used to be. An OR across two different columns cannot use either
-- index, so that form degraded into a FULL SEQUENTIAL SCAN of messages —
-- measured at 35ms over 221k rows, against an index-driven 1.7ms for this
-- version.
--
-- Speed is the lesser reason. Every transaction here runs at SERIALIZABLE, and
-- a sequential scan takes a predicate lock covering the ENTIRE table, so this
-- allocator conflicted with every concurrent message insert anywhere in the
-- system — not merely those in the same chat. That is what surfaced to users
-- as "failed to save mailbox envelope: ... could not serialize access due to
-- read/write dependencies among transactions (SQLSTATE 40001)" while several
-- spawns were saving messages at once. Restricting each branch to an index
-- narrows the predicate lock to the rows actually consulted.
--
-- `= ANY(ARRAY(...))` rather than `IN (SELECT ...)`: Postgres will not push a
-- recursive-CTE reference down into an index condition, so the IN form
-- re-introduced the seq scan. Materializing the chain into an array lets the
-- planner use idx_messages_context_window_ordinal.
WITH RECURSIVE chain AS (
    SELECT cw.id, cw.parent_context_window_id
    FROM context_windows cw
    WHERE cw.thread_id = $2
    UNION
    SELECT parent.id, parent.parent_context_window_id
    FROM context_windows parent
    JOIN chain ON chain.parent_context_window_id = parent.id
)
SELECT COALESCE(
    GREATEST(
        -- This chat's own rows (messages_chat_seq_key).
        (SELECT MAX(m.seq) FROM messages m WHERE m.chat_id = $1),
        -- Rows inherited through the fork chain
        -- (idx_messages_context_window_ordinal).
        (SELECT MAX(m.seq) FROM messages m
          WHERE m.context_window_id = ANY(ARRAY(SELECT id FROM chain)))
    ),
    -1
) + 1 AS next_seq;

-- name: GetNextOrdinalByContextWindow :one
-- Get next ordinal for a specific context window
SELECT COALESCE(MAX(ordinal), -1) + 1 AS next_ordinal
FROM messages
WHERE context_window_id = $1;

-- name: GetLatestMessageByThread :one
-- Get latest message in a thread (uses denormalized thread_id)
SELECT * FROM messages
WHERE thread_id = $1
ORDER BY seq DESC
LIMIT 1;

-- name: GetLatestMessageByContextWindow :one
-- Get latest message in a specific context window
SELECT * FROM messages
WHERE context_window_id = $1
ORDER BY seq DESC
LIMIT 1;

-- name: GetLatestContextSequenceByThread :one
-- Get the max context_window.sequence for a thread
SELECT COALESCE(MAX(cw.sequence), 0) AS max_context_sequence
FROM context_windows cw
WHERE cw.thread_id = $1;

-- name: CountMessagesByThread :one
-- Count messages in a thread (uses denormalized thread_id)
SELECT COUNT(*) AS count
FROM messages
WHERE thread_id = $1;

-- name: CountMessagesByContextWindow :one
-- Count messages in a specific context window
SELECT COUNT(*) AS count
FROM messages
WHERE context_window_id = $1;

-- name: CountMessagesByContextWindowUpToSeq :one
-- Count messages in a specific context window up to and including a seq
-- bound. Mirrors the fork filter in resolveMessagesFromCW (messages from
-- the direct parent CW keep only seq <= ForkAtMessageID's seq) but as a
-- COUNT instead of a row fetch, so CW-chain-aware totals can be computed
-- without materializing the chain.
SELECT COUNT(*) AS count
FROM messages
WHERE context_window_id = $1 AND seq <= $2;

-- name: ListRecentMessagesInContextWindowBeforeSeq :many
-- The newest `limit` messages in a single context window, strictly before
-- before_seq (0 means unbounded -- the newest page). Returned DESC so the
-- LIMIT keeps the newest rows; callers must reverse to restore ascending
-- order. Paired with HasMessagesBeforeInContextWindow for the cursor path's
-- hasMore check.
SELECT * FROM messages
WHERE context_window_id = sqlc.arg(context_window_id)
  AND (sqlc.arg(before_seq)::bigint = 0 OR seq < sqlc.arg(before_seq)::bigint)
ORDER BY seq DESC
LIMIT sqlc.arg(row_limit);

-- name: HasMessagesBeforeInContextWindow :one
-- Whether any message in this context window precedes before_seq. Used to
-- compute hasMore for the cursor-bounded read without fetching the rows.
SELECT EXISTS(
  SELECT 1 FROM messages
  WHERE context_window_id = $1 AND seq < $2
) AS has_more;

-- name: ListMessagesInContextWindowRange :many
-- Messages in a single context window with seq >= from_seq, ascending, and
-- optionally seq < to_seq (NULL means unbounded above). Used to bound a
-- sibling thread's read to the seq span the main thread's window actually
-- covers, instead of that thread's entire history. NULL mirrors the initial
-- (uncursored) snapshot's own window, which is unbounded above because spawn
-- threads out-write and out-live the main thread -- see ListRecentChatWindow.
SELECT * FROM messages
WHERE context_window_id = sqlc.arg(context_window_id)
  AND seq >= sqlc.arg(from_seq)
  AND (sqlc.narg(to_seq)::bigint IS NULL OR seq < sqlc.narg(to_seq))
ORDER BY seq ASC;

-- name: GetLatestMessageWithTokensByThread :one
-- Get the latest message with token data in a thread at a specific context sequence
SELECT m.* FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = $1
  AND cw.sequence = $2
  AND m.token_count IS NOT NULL
ORDER BY m.seq DESC
LIMIT 1;

-- name: GetMessagesByContextWindow :many
-- Get all messages in a context window
SELECT * FROM messages
WHERE context_window_id = $1
ORDER BY seq ASC;

-- name: GetMessagesByThreadAndSequence :many
-- Get messages for a thread at a specific context sequence
SELECT m.* FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = $1
  AND cw.sequence = $2
ORDER BY m.seq ASC;

-- name: ListRecentTranscriptSiblingMessages :many
-- The chat snapshot's sibling-thread rows: the newest row_limit messages at or
-- above from_seq that belong to neither the main thread (read separately,
-- through its context-window chain) nor any spawn thread.
--
-- Spawn threads are excluded outright. A spawn renders as one tool-call card
-- in its parent's transcript, and the card reads its child thread through
-- ListMessages(thread_id) when it is expanded, so spawn rows in the snapshot
-- are never rendered from. They were also most of it: spawn threads out-write
-- the main thread by an order of magnitude, so a chat-wide newest-N carried
-- whichever partial spawn transcripts happened to sit at the top of the seq
-- range (636KB of a 2.3MB snapshot on a real 55k-message chat).
--
-- What remains are the threads the transcript renders inline (workflow-node
-- and fork threads), bounded so a node-heavy chat cannot balloon the payload.
-- Returned DESC so the LIMIT keeps the newest rows; callers reverse.
SELECT m.* FROM messages m
WHERE m.chat_id = sqlc.arg(chat_id)
  AND m.thread_id <> sqlc.arg(main_thread_id)
  AND m.seq >= sqlc.arg(from_seq)
  AND m.thread_id NOT IN (
      SELECT t.id FROM threads t
      WHERE t.chat_id = sqlc.arg(chat_id) AND t.origin = 'spawn'
  )
ORDER BY m.seq DESC
LIMIT sqlc.arg(row_limit);

-- name: ListRecentMessagesByThread :many
-- Most recent N messages within a single thread. Returned DESC; reverse to
-- restore ascending order.
SELECT * FROM messages
WHERE thread_id = $1
ORDER BY seq DESC
LIMIT $2;

-- name: CountMessagesByChat :one
-- True total message count for a chat, so a bounded snapshot can report an
-- honest `total` rather than the length of the window it happened to send.
SELECT COUNT(*) AS count
FROM messages
WHERE chat_id = $1;

-- name: DeleteMessage :exec
DELETE FROM messages WHERE id = $1;