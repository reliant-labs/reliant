-- name: CreateThread :one
INSERT INTO threads (
    id, conversation_id, parent_thread_id, fork_at_ordinal, 
    fork_at_context_window_id, workflow_id, title, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetThread :one
SELECT * FROM threads WHERE id = ?;

-- name: GetThreadByWorkflow :one
SELECT * FROM threads WHERE workflow_id = ?;

-- name: ListThreadsByConversation :many
SELECT * FROM threads
WHERE conversation_id = ?
ORDER BY created_at ASC;

-- name: ListChildThreads :many
-- Get all threads that fork from a given thread (direct children only)
SELECT * FROM threads
WHERE parent_thread_id = ?
ORDER BY created_at ASC;

-- name: GetRootThread :one
-- Get the root thread for a conversation (no parent)
SELECT * FROM threads
WHERE conversation_id = ? AND parent_thread_id IS NULL
ORDER BY created_at ASC
LIMIT 1;

-- name: UpdateThreadWorkflow :one
UPDATE threads SET workflow_id = ?
WHERE id = ?
RETURNING *;

-- name: UpdateThreadForkPoint :one
-- Update fork point when context window changes (e.g., parent compacts)
UPDATE threads SET 
    fork_at_ordinal = ?,
    fork_at_context_window_id = ?
WHERE id = ?
RETURNING *;

-- name: DeleteThread :exec
DELETE FROM threads WHERE id = ?;

-- name: DeleteThreadsByConversation :exec
DELETE FROM threads WHERE conversation_id = ?;

-- name: GetThreadWithParent :one
-- Get thread with parent info for fork chain resolution
SELECT 
    t.*,
    pt.conversation_id AS parent_conversation_id
FROM threads t
LEFT JOIN threads pt ON t.parent_thread_id = pt.id
WHERE t.id = ?;

-- name: CountThreadsInConversation :one
SELECT COUNT(*) FROM threads WHERE conversation_id = ?;
