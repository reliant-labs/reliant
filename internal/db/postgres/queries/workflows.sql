-- name: CreateWorkflow :one
INSERT INTO workflows (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO NOTHING
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

-- name: SetWorkflowOutcome :one
-- Record the run's verdict (Node.outcome of the terminal node it reached).
-- Written once at completion and never reconciled from Temporal, unlike status:
-- a graph that routes to its `failed` node is a COMPLETED Temporal execution,
-- so the verdict has nowhere else to live.
UPDATE workflows SET
    outcome = $1
WHERE id = $2
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

-- name: GetRootWorkflowStatusForChat :one
-- Get effective workflow status for a single chat.
-- Returns 'running' if ANY real workflow (root or child) is running.
-- Returns 'paused' if ANY real workflow is paused (and none running).
-- Otherwise returns the most recent root workflow's status.
-- NOTE: Excludes thread metadata records ("thread:*" and "fork:*") - these track
-- thread lifecycle, not workflow execution. They complete when their owning workflow completes.
SELECT
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
    END AS status
FROM workflows w
WHERE w.parent_id IS NULL
  AND w.chat_id = $1
ORDER BY w.created_at DESC
LIMIT 1;

-- name: CascadeTerminalStatusToDescendants :exec
-- Move every DESCENDANT workflow record owned by a parent to the terminal
-- status the parent itself reached. Called when a workflow ends, to drain the
-- children (spawn children, thread records, etc.) that are not yet terminal.
--
-- The status is the caller's, not a constant. Writing a fixed "completed" here
-- laundered every terminated run into a finished one: a cancel terminated 23
-- descendants mid-flight and recorded all 23 as COMPLETED, so any later count
-- of completed units over-counted by the whole subtree. A descendant of a
-- cancelled run was cancelled; a descendant of a failed run did not complete
-- either. CHAT_WORKFLOW_STATUS already distinguishes them (3=completed,
-- 4=failed, 5=cancelled, 7=expired) — nothing new needs inventing, the write
-- just has to stop discarding what it knows.
--
-- Recursive: a spawn's own spawns (grandchildren and deeper) must end too — a
-- one-level cascade leaves them running/paused forever, which keeps the chat
-- permanently "active" in chats_with_activity and (for paused rows) permanently
-- exempt from the progress watchdog.
-- Matches running (2) and paused (6) descendants.
WITH RECURSIVE descendants AS (
    SELECT c.id FROM workflows c WHERE c.parent_id = $1
    UNION ALL
    SELECT g.id FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
)
UPDATE workflows AS t
SET status = $2, completed_at = NOW()
FROM descendants
WHERE t.id = descendants.id
  AND t.status IN (2, 6);

-- name: ReapOrphanedWorkflowDescendants :execrows
-- Enforce the invariant CascadeTerminalStatusToDescendants asserts from the
-- other direction: a workflow whose PARENT is terminal is not running.
--
-- CascadeTerminalStatusToDescendants only runs when the write path that moved a
-- parent to a terminal status remembers to call it. Several do not, and each
-- omission strands the whole subtree at running (2) / paused (6) forever:
-- nothing else ever revisits those rows, because the reconciler skips every
-- workflow with a parent_id (its lifecycle is the parent's job) and
-- `workflow ps` filters on status alone, so the orphans are reported as live
-- runs next to real ones.
--
-- The known omissions this repairs are TerminateWorkflow paths. A terminate is
-- a hard kill: the workflow's own completion handler never runs, so the
-- cascade its terminal-status activity would have performed never happens.
-- Every caller is expected to cascade for itself — this is the backstop that
-- keeps one forgotten call site from stranding rows permanently.
--
-- Anchored on ANY terminal parent, not just roots, so a terminal mid-tree
-- spawn drains its own subtree. Inherits the terminal ANCESTOR's status, which
-- is what the direct cascade writes, so a row reaped here stays
-- indistinguishable from one cascaded there — including the distinction
-- between a run that finished and one that was terminated.
WITH RECURSIVE descendants AS (
    SELECT c.id, p.status AS terminal_status FROM workflows c
    JOIN workflows p ON c.parent_id = p.id
    WHERE p.status IN (3, 4, 5, 7)
    UNION ALL
    SELECT g.id, d.terminal_status FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
)
UPDATE workflows AS t
SET status = descendants.terminal_status, completed_at = NOW()
FROM descendants
WHERE t.id = descendants.id
  AND t.status IN (2, 6);

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