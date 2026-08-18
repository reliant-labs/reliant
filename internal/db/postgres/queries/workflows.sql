-- name: CreateWorkflow :one
INSERT INTO workflows (
    id, parent_id, chat_id, workflow_name, thread, state, stop_reason,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
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
-- Update workflow lifecycle with conditional timestamp handling:
-- - Stopped for good (completed/failed/cancelled): set completed_at
-- - Active/Pending: clear completed_at
-- - Stopped-but-paused: leave completed_at alone; the run has not ended.
UPDATE workflows SET
    state = $1,
    stop_reason = $2,
    completed_at = CASE
        WHEN $1 = 3 AND $2 <> 3 THEN NOW()
        WHEN $1 IN (1, 2) THEN NULL
        ELSE completed_at
    END
WHERE id = $3
RETURNING *;

-- name: CompareAndSwapWorkflowStatus :one
-- Atomically update workflow lifecycle only if the current one matches
-- expected. Returns the updated row if swapped, sql.ErrNoRows if it did not
-- match.
--
-- The comparison is over the PAIR: two rows that are both STOPPED but stopped
-- for different reasons are different statuses, and a swap expecting one must
-- not win against the other.
UPDATE workflows SET
    state = $1,
    stop_reason = $2,
    completed_at = CASE
        WHEN $1 = 3 AND $2 <> 3 THEN NOW()
        WHEN $1 IN (1, 2) THEN NULL
        ELSE completed_at
    END
WHERE id = $3 AND state = $4 AND stop_reason = $5
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
-- Update workflow name (only allowed while the run is still PENDING)
UPDATE workflows SET
    workflow_name = $1
WHERE id = $2 AND state = 1
RETURNING *;

-- name: DeleteWorkflow :exec
DELETE FROM workflows WHERE id = $1;

-- name: DeleteWorkflowsByChat :exec
DELETE FROM workflows WHERE chat_id = $1;

-- name: GetRootWorkflowStatusForChat :one
-- Get effective workflow lifecycle for a single chat.
-- Returns ACTIVE if ANY real workflow (root or child) is active.
-- Returns STOPPED/PAUSED if ANY real workflow is paused (and none active).
-- Otherwise returns the most recent root workflow's own lifecycle.
-- Every row in workflows is now a real workflow execution: thread lifecycle
-- lives on threads.status, so the "thread:*"/"fork:*" name filters this query
-- used to carry are gone along with the records they excluded.
SELECT
    w.chat_id,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM workflows w3
            WHERE w3.chat_id = w.chat_id
              AND w3.state = 2
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM workflows w4
            WHERE w4.chat_id = w.chat_id
              AND w4.state = 3 AND w4.stop_reason = 3
        ) THEN 3
        ELSE w.state
    END AS state,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM workflows w3
            WHERE w3.chat_id = w.chat_id
              AND w3.state = 2
        ) THEN 0
        WHEN EXISTS (
            SELECT 1 FROM workflows w4
            WHERE w4.chat_id = w.chat_id
              AND w4.state = 3 AND w4.stop_reason = 3
        ) THEN 3
        ELSE w.stop_reason
    END AS stop_reason
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
-- The stop reason is the caller's, not a constant. Writing a fixed "completed"
-- here laundered every terminated run into a finished one: a cancel terminated
-- 23 descendants mid-flight and recorded all 23 as COMPLETED, so any later
-- count of completed units over-counted by the whole subtree. A descendant of
-- a cancelled run was cancelled; a descendant of a failed run did not complete
-- either. The stop reason already distinguishes them — nothing new needs
-- inventing, the write just has to stop discarding what it knows.
--
-- Recursive: a spawn's own spawns (grandchildren and deeper) must end too — a
-- one-level cascade leaves them active/paused forever, which keeps the chat
-- permanently "active" in chats_with_activity and (for paused rows) permanently
-- exempt from the progress watchdog.
-- Matches every LIVE descendant: active (state 2) and paused (3/3).
WITH RECURSIVE descendants AS (
    SELECT c.id FROM workflows c WHERE c.parent_id = $1
    UNION ALL
    SELECT g.id FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
)
UPDATE workflows AS t
SET state = 3, stop_reason = $2, completed_at = NOW()
FROM descendants
WHERE t.id = descendants.id
  AND (t.state = 2 OR (t.state = 3 AND t.stop_reason = 3));

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
-- spawn drains its own subtree. Inherits the terminal ANCESTOR's stop reason,
-- which is what the direct cascade writes, so a row reaped here stays
-- indistinguishable from one cascaded there — including the distinction
-- between a run that finished and one that was terminated.
--
-- "Terminal parent" is STOPPED for a reason other than PAUSED: a paused parent
-- has not ended, and reaping its children would kill a run that is coming back.
WITH RECURSIVE descendants AS (
    SELECT c.id, p.stop_reason AS terminal_reason FROM workflows c
    JOIN workflows p ON c.parent_id = p.id
    WHERE p.state = 3 AND p.stop_reason <> 3
    UNION ALL
    SELECT g.id, d.terminal_reason FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
)
UPDATE workflows AS t
SET state = 3, stop_reason = descendants.terminal_reason, completed_at = NOW()
FROM descendants
WHERE t.id = descendants.id
  AND (t.state = 2 OR (t.state = 3 AND t.stop_reason = 3));

-- name: ListWorkflowsByStatus :many
-- List all workflows at a specific lifecycle (e.g. ACTIVE, or STOPPED/PAUSED).
-- Used for startup recovery to restart workers for active workflows.
SELECT * FROM workflows
WHERE state = $1 AND stop_reason = $2
ORDER BY created_at ASC;

-- name: ListRootWorkflowsByStatus :many
-- List root workflows (parent_id IS NULL) at a specific lifecycle.
-- Root workflows are the entry points that need dedicated workers.
SELECT * FROM workflows
WHERE parent_id IS NULL AND state = $1 AND stop_reason = $2
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
-- Pause all active workflows for a chat.
-- Used when pausing a chat to ensure child workflows (e.g., agent threads) are also paused,
-- so the chats_with_activity view correctly reports the chat as paused.
UPDATE workflows SET state = 3, stop_reason = 3
WHERE chat_id = $1 AND state = 2;

-- name: ResumeWorkflowsByChat :exec
-- Resume all paused workflows for a chat.
-- Used when resuming a chat to ensure child workflows are also resumed.
UPDATE workflows SET state = 2, stop_reason = 0
WHERE chat_id = $1 AND state = 3 AND stop_reason = 3;

-- NOTE: Recursive ancestor/descendant queries can be implemented in Go code
-- using ListChildWorkflows and GetWorkflow iteratively, or via raw SQL if needed.