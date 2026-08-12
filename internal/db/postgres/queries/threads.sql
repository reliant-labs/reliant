-- name: CreateThread :one
INSERT INTO threads (
    id, chat_id, parent_thread_id, fork_at_message_id,
    workflow_id, title, created_at,
    origin, origin_node_id, status
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetThread :one
SELECT * FROM threads WHERE id = $1;

-- name: GetThreadByWorkflow :one
SELECT * FROM threads WHERE workflow_id = $1;

-- name: ListThreadsByConversation :many
SELECT * FROM threads
WHERE chat_id = $1
ORDER BY created_at ASC;

-- name: ListChildThreads :many
-- Get all threads that fork from a given thread (direct children only)
SELECT * FROM threads
WHERE parent_thread_id = $1
ORDER BY created_at ASC;

-- name: GetRootThread :one
-- Get the root thread for a chat (no parent)
SELECT * FROM threads
WHERE chat_id = $1 AND parent_thread_id IS NULL
ORDER BY created_at ASC
LIMIT 1;

-- name: UpdateThreadWorkflow :one
UPDATE threads SET workflow_id = $1
WHERE id = $2
RETURNING *;

-- name: UpdateThreadForkPoint :one
-- Update fork point when the fork's target message changes (e.g., parent compacts)
UPDATE threads SET 
    fork_at_message_id = $1
WHERE id = $2
RETURNING *;

-- name: UpdateThreadStatus :one
-- Record thread lifecycle. Replaces the "thread:<node>" workflow records that
-- used to carry this: threads own their own start/finish now.
-- completed_at is only meaningful for terminal statuses; callers pass NULL
-- when moving a thread back to running.
UPDATE threads SET
    status = $1,
    completed_at = $2
WHERE id = $3
RETURNING *;

-- name: ListThreadsByOrigin :many
-- Threads in a chat with a given origin (e.g. every spawn thread).
SELECT * FROM threads
WHERE chat_id = $1 AND origin = $2
ORDER BY created_at ASC;

-- name: DeleteThread :exec
DELETE FROM threads WHERE id = $1;

-- name: DeleteThreadsByConversation :exec
DELETE FROM threads WHERE chat_id = $1;

-- name: GetThreadWithParent :one
-- Get thread with parent info for fork chain resolution
SELECT 
    t.*,
    pt.chat_id AS parent_chat_id
FROM threads t
LEFT JOIN threads pt ON t.parent_thread_id = pt.id
WHERE t.id = $1;

-- name: CountThreadsInConversation :one
SELECT COUNT(*) FROM threads WHERE chat_id = $1;
