-- name: CreateContextWindow :one
INSERT INTO context_windows (
    id, thread_id, sequence, 
    parent_context_window_id, fork_at_ordinal,
    compaction_summary_message_id, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetContextWindow :one
SELECT * FROM context_windows WHERE id = $1;

-- name: GetLatestContextWindow :one
-- Get the most recent context window for a thread (highest sequence)
SELECT * FROM context_windows
WHERE thread_id = $1
ORDER BY sequence DESC
LIMIT 1;

-- name: GetContextWindowBySequence :one
SELECT * FROM context_windows
WHERE thread_id = $1 AND sequence = $2;

-- name: ListContextWindowsByThread :many
SELECT * FROM context_windows
WHERE thread_id = $1
ORDER BY sequence ASC;

-- name: SetCompactionSummaryMessage :one
UPDATE context_windows SET compaction_summary_message_id = $1
WHERE id = $2
RETURNING *;

-- name: DeleteContextWindow :exec
DELETE FROM context_windows WHERE id = $1;

-- name: DeleteContextWindowsByThread :exec
DELETE FROM context_windows WHERE thread_id = $1;

-- name: GetMaxSequenceForThread :one
SELECT COALESCE(MAX(sequence), -1) AS max_sequence
FROM context_windows
WHERE thread_id = $1;

-- name: GetContextWindowWithThread :one
-- Get context window with thread info for resolution
SELECT 
    cw.*,
    t.conversation_id,
    t.parent_thread_id,
    t.fork_at_ordinal
FROM context_windows cw
JOIN threads t ON t.id = cw.thread_id
WHERE cw.id = $1;