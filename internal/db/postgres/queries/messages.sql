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
WITH RECURSIVE chain AS (
    SELECT cw.id, cw.parent_context_window_id
    FROM context_windows cw
    WHERE cw.thread_id = $2
    UNION
    SELECT parent.id, parent.parent_context_window_id
    FROM context_windows parent
    JOIN chain ON chain.parent_context_window_id = parent.id
)
SELECT COALESCE(MAX(m.seq), -1) + 1 AS next_seq
FROM messages m
WHERE m.chat_id = $1
   OR m.context_window_id IN (SELECT id FROM chain);

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

-- name: ListRecentMessages :many
-- Most recent N messages for a chat, for the bounded initial chat snapshot.
-- Ordered DESC so the LIMIT keeps the NEWEST rows; callers must reverse to get
-- the ascending order every other consumer expects.
SELECT * FROM messages
WHERE chat_id = $1
ORDER BY seq DESC
LIMIT $2;

-- name: ListRecentMessagesByThread :many
-- Most recent N messages within a single thread. Returned DESC; reverse to
-- restore ascending order.
SELECT * FROM messages
WHERE thread_id = $1
ORDER BY seq DESC
LIMIT $2;

-- name: ListRecentChatWindow :many
-- The initial chat snapshot's window: the newest N messages ON THE MAIN THREAD,
-- plus every message from any other thread that falls inside that seq range.
--
-- Bounding the window by the whole chat (ListRecentMessages) is wrong once a
-- chat spawns sub-agents. Spawn threads write far more messages than the main
-- thread and finish later, so they occupy the top of the chat's seq range: in a
-- real 1,470-message chat the newest 200 rows were 200 spawn messages and ZERO
-- main-thread messages. Spawn messages render collapsed inside the tool call
-- that created them rather than in the transcript, so that snapshot painted an
-- empty conversation.
--
-- The main thread is what the transcript shows, so it is what the window must
-- be measured in. Sibling-thread messages inside the resulting range still come
-- along, because the spawn tool-call preview renders from them.
WITH main_window AS (
    SELECT mw.seq
    FROM messages mw
    WHERE mw.chat_id = $1 AND mw.thread_id = $2
    ORDER BY mw.seq DESC
    LIMIT $3
)
SELECT m.* FROM messages m
WHERE m.chat_id = $1
  AND m.seq >= (SELECT COALESCE(MIN(main_window.seq), 0) FROM main_window)
ORDER BY m.seq DESC;

-- name: CountMessagesByChat :one
-- True total message count for a chat, so a bounded snapshot can report an
-- honest `total` rather than the length of the window it happened to send.
SELECT COUNT(*) AS count
FROM messages
WHERE chat_id = $1;

-- name: DeleteMessage :exec
DELETE FROM messages WHERE id = $1;