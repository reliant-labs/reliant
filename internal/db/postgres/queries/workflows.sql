-- name: CreateWorkflow :one
INSERT INTO workflows (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetWorkflow :one
SELECT * FROM workflows WHERE id = $1;

-- name: GetWorkflowByThread :one
SELECT * FROM workflows 
WHERE chat_id = $1 AND thread = $2;

-- name: ListWorkflowsByChat :many
SELECT * FROM workflows
WHERE chat_id = $1
ORDER BY created_at ASC;

-- name: ListChildWorkflows :many
SELECT * FROM workflows
WHERE parent_id = $1
ORDER BY created_at ASC;

-- name: ListRootWorkflows :many
SELECT * FROM workflows
WHERE chat_id = $1 AND parent_id IS NULL
ORDER BY created_at DESC;

-- name: UpdateWorkflowStatus :one
-- Update workflow status with conditional timestamp handling:
-- - Terminal states (completed/failed/cancelled): set completed_at
-- - Running/Pending: clear completed_at
UPDATE workflows SET
    status = $1,
    completed_at = CASE
        WHEN $1 IN (3, 4, 5) THEN NOW()
        WHEN $1 IN (2, 1) THEN NULL
        ELSE completed_at
    END
WHERE id = $2
RETURNING *;

-- name: CompareAndSwapWorkflowStatus :one
-- Atomically update workflow status only if current status matches expected.
-- Returns the updated row if swapped, sql.ErrNoRows if status didn't match.
UPDATE workflows SET
    status = $1,
    completed_at = CASE
        WHEN $1 IN (3, 4, 5) THEN NOW()
        WHEN $1 IN (2, 1) THEN NULL
        ELSE completed_at
    END
WHERE id = $2 AND status = $3
RETURNING *;

-- name: UpdateWorkflowName :one
-- Update workflow name (only allowed when status is pending)
UPDATE workflows SET
    workflow_name = $1
WHERE id = $2 AND status = 1
RETURNING *;

-- name: DeleteWorkflow :exec
DELETE FROM workflows WHERE id = $1;

-- name: DeleteWorkflowsByChat :exec
DELETE FROM workflows WHERE chat_id = $1;

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
  AND w.chat_id = ANY(sqlc.arg('chat_ids')::text[])
  AND w.created_at = (
    SELECT MAX(w2.created_at) 
    FROM workflows w2 
    WHERE w2.chat_id = w.chat_id AND w2.parent_id IS NULL
  )
ORDER BY w.chat_id;

-- name: CompleteChildWorkflows :exec
-- Complete all child workflow records owned by a parent workflow.
-- Called when a workflow reaches a terminal state to cascade completion to all
-- children (spawn children, thread records, etc.) that are not yet terminal.
-- Matches running (2) and paused (6) children — prevents orphaned children
-- from keeping the chat permanently stuck as "active".
UPDATE workflows 
SET status = 3, completed_at = NOW()
WHERE parent_id = $1 
  AND status IN (2, 6);

-- name: ListWorkflowsByStatus :many
-- List all workflows with a specific status (e.g., 2=running, 6=paused).
-- Used for startup recovery to restart workers for active workflows.
SELECT * FROM workflows
WHERE status = $1
ORDER BY created_at ASC;

-- name: ListRootWorkflowsByStatus :many
-- List root workflows (parent_id IS NULL) with a specific status.
-- Root workflows are the entry points that need dedicated workers.
SELECT * FROM workflows
WHERE parent_id IS NULL AND status = $1
ORDER BY created_at ASC;

-- name: UpdateWorkflowWorkerStarted :exec
-- Record when a worker was started for this workflow.
-- Clears worker_stopped_at since worker is now running.
-- Used for reconciler race condition fix: skip stuck check for recently started workers.
UPDATE workflows SET
    worker_started_at = NOW(),
    worker_stopped_at = NULL
WHERE id = $1;

-- name: UpdateWorkflowWorkerStopped :exec
-- Record when a worker was stopped for this workflow.
-- Used for tracking worker lifecycle (e.g., during pause).
UPDATE workflows SET
    worker_stopped_at = NOW()
WHERE id = $1;

-- name: PauseRunningWorkflowsByChat :exec
-- Pause all running workflows for a chat.
-- Used when pausing a chat to ensure child workflows (e.g., agent threads) are also paused,
-- so the chats_with_activity view correctly reports the chat as paused.
UPDATE workflows SET status = 6
WHERE chat_id = $1 AND status = 2;

-- name: ResumeWorkflowsByChat :exec
-- Resume all paused workflows for a chat.
-- Used when resuming a chat to ensure child workflows are also resumed.
UPDATE workflows SET status = 2
WHERE chat_id = $1 AND status = 6;

-- NOTE: Recursive ancestor/descendant queries can be implemented in Go code
-- using ListChildWorkflows and GetWorkflow iteratively, or via raw SQL if needed.