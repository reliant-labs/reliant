-- name: CreateThread :one
INSERT INTO threads (
    id, conversation_id, parent_thread_id, fork_at_ordinal, 
    fork_at_context_window_id, workflow_id, title, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetThread :one
SELECT * FROM threads WHERE id = $1;

-- name: GetThreadByWorkflow :one
SELECT * FROM threads WHERE workflow_id = $1;

-- name: ListThreadsByConversation :many
SELECT * FROM threads
WHERE conversation_id = $1
ORDER BY created_at ASC;

-- name: ListChildThreads :many
-- Get all threads that fork from a given thread (direct children only)
SELECT * FROM threads
WHERE parent_thread_id = $1
ORDER BY created_at ASC;

-- name: GetRootThread :one
-- Get the root thread for a conversation (no parent)
SELECT * FROM threads
WHERE conversation_id = $1 AND parent_thread_id IS NULL
ORDER BY created_at ASC
LIMIT 1;

-- name: UpdateThreadWorkflow :one
UPDATE threads SET workflow_id = $1
WHERE id = $2
RETURNING *;

-- name: UpdateThreadForkPoint :one
-- Update fork point when context window changes (e.g., parent compacts)
UPDATE threads SET 
    fork_at_ordinal = $1,
    fork_at_context_window_id = $2
WHERE id = $3
RETURNING *;

-- name: DeleteThread :exec
DELETE FROM threads WHERE id = $1;

-- name: DeleteThreadsByConversation :exec
DELETE FROM threads WHERE conversation_id = $1;

-- name: GetThreadWithParent :one
-- Get thread with parent info for fork chain resolution
SELECT 
    t.*,
    pt.conversation_id AS parent_conversation_id
FROM threads t
LEFT JOIN threads pt ON t.parent_thread_id = pt.id
WHERE t.id = $1;

-- name: CountThreadsInConversation :one
SELECT COUNT(*) FROM threads WHERE conversation_id = $1;
