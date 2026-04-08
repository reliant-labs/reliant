-- name: CreateWorkflow :one
INSERT INTO workflows (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at;

-- name: GetWorkflow :one
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE id = ?;

-- name: GetWorkflowByThread :one
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE chat_id = ? AND thread = ?;

-- name: ListWorkflowsByChat :many
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE chat_id = ?
ORDER BY created_at ASC;

-- name: ListChildWorkflows :many
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE parent_id = ?
ORDER BY created_at ASC;

-- name: ListRootWorkflows :many
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE chat_id = ? AND parent_id IS NULL
ORDER BY created_at DESC;

-- name: UpdateWorkflowStatus :one
-- Update workflow status with conditional timestamp handling:
-- - Terminal states (completed/failed/cancelled): set completed_at
-- - Running/Pending: clear completed_at
-- Note: Uses ?1 numbered parameter to reuse the status value in CASE expressions
UPDATE workflows SET
    status = ?1,
    completed_at = CASE
        WHEN ?1 IN (3, 4, 5) THEN datetime('now', 'utc')
        WHEN ?1 IN (2, 1) THEN NULL
        ELSE completed_at
    END
WHERE id = ?2
RETURNING id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at;

-- name: CompareAndSwapWorkflowStatus :one
-- Atomically update workflow status only if current status matches expected.
-- Returns the updated row if swapped, sql.ErrNoRows if status didn't match.
UPDATE workflows SET
    status = ?1,
    completed_at = CASE
        WHEN ?1 IN (3, 4, 5) THEN datetime('now', 'utc')
        WHEN ?1 IN (2, 1) THEN NULL
        ELSE completed_at
    END
WHERE id = ?2 AND status = ?3
RETURNING id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at;

-- name: UpdateWorkflowName :one
-- Update workflow name (only allowed when status is pending)
UPDATE workflows SET
    workflow_name = ?
WHERE id = ? AND status = 1
RETURNING id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at;

-- name: DeleteWorkflow :exec
DELETE FROM workflows WHERE id = ?;

-- name: DeleteWorkflowsByChat :exec
DELETE FROM workflows WHERE chat_id = ?;

-- name: GetRootWorkflowStatusForChats :many
-- Get effective workflow status for multiple chats
-- Returns 'running' if ANY real workflow (root or child) is running
-- Returns 'paused' if ANY real workflow is paused (and none running)
-- Otherwise returns the most recent root workflow's status
-- NOTE: Excludes thread metadata records ("thread:*" and "fork:*") - these track
-- thread lifecycle, not workflow execution. They complete when their owning workflow completes.
SELECT DISTINCT
    w.chat_id,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM workflows w3
            WHERE w3.chat_id = w.chat_id
              AND w3.status = 2
              AND w3.workflow_name NOT LIKE 'thread:%'
              AND w3.workflow_name NOT LIKE 'fork:%'
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM workflows w4
            WHERE w4.chat_id = w.chat_id
              AND w4.status = 6
              AND w4.workflow_name NOT LIKE 'thread:%'
              AND w4.workflow_name NOT LIKE 'fork:%'
        ) THEN 6
        ELSE w.status
    END as status
FROM workflows w
WHERE w.parent_id IS NULL
  AND w.chat_id IN (/*SLICE:chat_ids*/sqlc.slice('chat_ids'))
  AND w.created_at = (
    SELECT MAX(w2.created_at)
    FROM workflows w2
    WHERE w2.chat_id = w.chat_id AND w2.parent_id IS NULL
  )
ORDER BY w.chat_id;

-- name: CompleteRunningChildWorkflows :exec
-- Complete running child workflow records owned by a parent workflow.
UPDATE workflows
SET status = 3, completed_at = datetime('now', 'utc')
WHERE parent_id = ?
  AND status = 2;

-- name: CompletePausedChildWorkflows :exec
-- Complete paused child workflow records owned by a parent workflow.
UPDATE workflows
SET status = 3, completed_at = datetime('now', 'utc')
WHERE parent_id = ?
  AND status = 6;

-- name: ListWorkflowsByStatus :many
-- List all workflows with a specific status (e.g., 2=running, 6=paused).
-- Used for startup recovery to restart workers for active workflows.
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE status = ?
ORDER BY created_at ASC;

-- name: ListRootWorkflowsByStatus :many
-- List root workflows (parent_id IS NULL) with a specific status.
-- Root workflows are the entry points that need dedicated workers.
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, created_at, completed_at
FROM workflows
WHERE parent_id IS NULL AND status = ?
ORDER BY created_at ASC;

-- name: PauseRunningWorkflowsByChat :exec
-- Pause all running workflows for a chat.
-- Used when pausing a chat to ensure child workflows (e.g., agent threads) are also paused,
-- so the chats_with_activity view correctly reports the chat as paused.
UPDATE workflows SET status = 6
WHERE chat_id = ? AND status = 2;

-- name: ResumeWorkflowsByChat :exec
-- Resume all paused workflows for a chat.
-- Used when resuming a chat to ensure child workflows are also resumed.
UPDATE workflows SET status = 2
WHERE chat_id = ? AND status = 6;

-- NOTE: Recursive ancestor/descendant queries can be implemented in Go code
-- using ListChildWorkflows and GetWorkflow iteratively, or via raw SQL if needed.