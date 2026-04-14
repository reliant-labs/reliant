-- name: CreateMessage :exec
-- Creates a message in the new schema (uses context_window_id, has display_style)
INSERT INTO messages (
    id, chat_id, ordinal, thread_id, context_window_id,
    node_id, node_path,
    role, display_style, model, agent,
    token_count, cost_micros,
    workflow_id, run_id, activity_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetMessage :one
SELECT * FROM messages WHERE id = ?;

-- name: ListMessages :many
SELECT * FROM messages
WHERE chat_id = ?
ORDER BY ordinal ASC;

-- name: UpdateMessage :exec
UPDATE messages SET
    token_count = ?,
    cost_micros = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: GetMessageByActivityID :one
SELECT * FROM messages
WHERE chat_id = ? AND activity_id = ?
LIMIT 1;

-- name: CreateMessageIfNotExists :exec
-- INSERT OR IGNORE for idempotent message creation
INSERT OR IGNORE INTO messages (
    id, chat_id, ordinal, thread_id, context_window_id,
    node_id, node_path,
    role, display_style, model, agent,
    token_count, cost_micros,
    workflow_id, run_id, activity_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetNextOrdinalByThread :one
-- Get next ordinal for a thread (uses denormalized thread_id)
SELECT COALESCE(MAX(ordinal), -1) + 1 AS next_ordinal
FROM messages
WHERE thread_id = ?;

-- name: GetNextOrdinalByContextWindow :one
-- Get next ordinal for a specific context window
SELECT COALESCE(MAX(ordinal), -1) + 1 AS next_ordinal
FROM messages
WHERE context_window_id = ?;

-- name: GetLatestMessageByThread :one
-- Get latest message in a thread (uses denormalized thread_id)
SELECT * FROM messages
WHERE thread_id = ?
ORDER BY ordinal DESC
LIMIT 1;

-- name: GetLatestMessageByContextWindow :one
-- Get latest message in a specific context window
SELECT * FROM messages
WHERE context_window_id = ?
ORDER BY ordinal DESC
LIMIT 1;

-- name: GetLatestContextSequenceByThread :one
-- Get the max context_window.sequence for a thread
SELECT COALESCE(MAX(cw.sequence), 0) AS max_context_sequence
FROM context_windows cw
WHERE cw.thread_id = ?;

-- name: CountMessagesByThread :one
-- Count messages in a thread (uses denormalized thread_id)
SELECT COUNT(*) AS count
FROM messages
WHERE thread_id = ?;

-- name: CountMessagesByContextWindow :one
-- Count messages in a specific context window
SELECT COUNT(*) AS count
FROM messages
WHERE context_window_id = ?;

-- name: GetLatestMessageWithTokensByThread :one
-- Get the latest message with token data in a thread at a specific context sequence
SELECT m.* FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = ?
  AND cw.sequence = ?
  AND m.token_count IS NOT NULL
ORDER BY m.ordinal DESC
LIMIT 1;

-- name: GetMessagesByContextWindow :many
-- Get all messages in a context window
SELECT * FROM messages
WHERE context_window_id = ?
ORDER BY ordinal ASC;

-- name: GetMessagesByThreadAndSequence :many
-- Get messages for a thread at a specific context sequence
SELECT m.* FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = ?
  AND cw.sequence = ?
ORDER BY m.ordinal ASC;

-- name: DeleteMessage :exec
DELETE FROM messages WHERE id = ?;