-- name: CreateMessage :exec
-- Creates a message in the new schema (uses context_window_id, has display_style)
INSERT INTO messages (
    id, chat_id, ordinal, thread_id, context_window_id,
    node_id, node_path,
    role, display_style, model, agent,
    token_count, cost_micros,
    workflow_id, run_id, activity_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18);

-- name: GetMessage :one
SELECT * FROM messages WHERE id = $1;

-- name: ListMessages :many
SELECT * FROM messages
WHERE chat_id = $1
ORDER BY ordinal ASC;

-- name: UpdateMessage :exec
UPDATE messages SET
    token_count = $1,
    cost_micros = $2,
    updated_at = NOW()
WHERE id = $3;

-- name: GetMessageByActivityID :one
SELECT * FROM messages
WHERE chat_id = $1 AND activity_id = $2
LIMIT 1;

-- name: CreateMessageIfNotExists :exec
-- ON CONFLICT DO NOTHING for idempotent message creation
INSERT INTO messages (
    id, chat_id, ordinal, thread_id, context_window_id,
    node_id, node_path,
    role, display_style, model, agent,
    token_count, cost_micros,
    workflow_id, run_id, activity_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT(id) DO NOTHING;

-- name: GetNextOrdinalByThread :one
-- Get next ordinal for a thread (uses denormalized thread_id)
SELECT COALESCE(MAX(ordinal), -1) + 1 AS next_ordinal
FROM messages
WHERE thread_id = $1;

-- name: GetNextOrdinalByContextWindow :one
-- Get next ordinal for a specific context window
SELECT COALESCE(MAX(ordinal), -1) + 1 AS next_ordinal
FROM messages
WHERE context_window_id = $1;

-- name: GetLatestMessageByThread :one
-- Get latest message in a thread (uses denormalized thread_id)
SELECT * FROM messages
WHERE thread_id = $1
ORDER BY ordinal DESC
LIMIT 1;

-- name: GetLatestMessageByContextWindow :one
-- Get latest message in a specific context window
SELECT * FROM messages
WHERE context_window_id = $1
ORDER BY ordinal DESC
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

-- name: GetLatestMessageWithTokensByThread :one
-- Get the latest message with token data in a thread at a specific context sequence
SELECT m.* FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = $1
  AND cw.sequence = $2
  AND m.token_count IS NOT NULL
ORDER BY m.ordinal DESC
LIMIT 1;

-- name: GetMessagesByContextWindow :many
-- Get all messages in a context window
SELECT * FROM messages
WHERE context_window_id = $1
ORDER BY ordinal ASC;

-- name: GetMessagesByThreadAndSequence :many
-- Get messages for a thread at a specific context sequence
SELECT m.* FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = $1
  AND cw.sequence = $2
ORDER BY m.ordinal ASC;

-- name: DeleteMessage :exec
DELETE FROM messages WHERE id = $1;