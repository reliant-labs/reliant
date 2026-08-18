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

-- name: ReviveThread :execrows
-- Move a thread back to running because a new run has started on it.
--
-- The exact inverse of CascadeTerminalStatusToThreadSubtree below, and the
-- missing half of a lifecycle that was only ever written in one direction. A
-- chat's MAIN thread is reused for every turn: SendMessage starts a fresh
-- Temporal run under the same workflow ID and the same thread ID. The end of
-- turn N stamps the thread terminal, and WorkflowStatusActivity's "started"
-- arm moves the WORKFLOW row back to running for turn N+1 -- but nothing
-- moved the THREAD, so from the second turn onward every chat ran with a live
-- workflow behind a thread row still reading completed.
--
-- That is what made SendAgentMessage refuse a live agent with "This agent has
-- already finished": its terminal-thread check is a correct question asked of
-- a field that was never reset. Measured on the live DB at the time of the
-- fix: 3 chats with a RUNNING workflow and a terminal main thread.
--
-- completed_at goes back to NULL with the status. A revived thread that keeps
-- the completion timestamp of a turn that already ended is a row that reads
-- as "finished at 16:31" while it is executing.
--
-- Guarded to terminal statuses only (3,4,5,7), which makes it a no-op for a
-- thread already running or paused rather than a write that clears a live
-- thread's bookkeeping. Reports rows moved so the caller can log a real
-- revival without a second read.
UPDATE threads
SET status = 2, completed_at = NULL
WHERE id = $1
  AND status IN (3, 4, 5, 7);

-- name: ListThreadsByOrigin :many
-- Threads in a chat with a given origin (e.g. every spawn thread).
SELECT * FROM threads
WHERE chat_id = $1 AND origin = $2
ORDER BY created_at ASC;

-- name: CascadeTerminalStatusToThreadSubtree :exec
-- The thread-lifecycle counterpart to CascadeTerminalStatusToDescendants
-- (workflows.sql). threads.status is otherwise written ONLY by
-- ThreadStatusActivity on the live path (thread_status.go) -- so an abnormal
-- exit (terminate, reap, a status write that races past the thread's own
-- "completed" activity call) leaves the thread at running (2) forever, with
-- completed_at NULL. Measured on the live DB (see
-- docs/incidents/2026-08-12-spawn-history-cap.md): 288 threads stranded at
-- status=2 under an already-terminal workflow -- 174 whose workflow completed,
-- 64 cancelled, 50 failed.
--
-- This is not cosmetic. ListThreadsWithOrphanedAgentMessages
-- (agent_messages.sql) -- the sweep that resolves a dead thread's queued
-- mailbox rows -- only matches threads already in a TERMINAL status. A thread
-- stuck at running makes its own orphaned mailbox permanently invisible to
-- that sweep, on top of misreporting the thread itself as still active.
--
-- Every call site pairs UpdateWorkflowStatus($1, status) [closes the workflow
-- named by $1 itself] with CascadeTerminalStatusToDescendants($1, status)
-- [closes only its DESCENDANTS]. This mirrors that exact pair in one query:
-- the subtree CTE starts AT $1 (so $1's own thread closes too, matching the
-- UpdateWorkflowStatus half) and walks parent_id recursively (matching the
-- Descendants half) -- one call, issued alongside the existing cascade call,
-- closes every thread the pair together just terminated.
--
-- status IN (2, 6) is the same fail-closed guard the workflow cascade
-- applies: an already-terminal thread is left untouched.
WITH RECURSIVE subtree AS (
    SELECT sqlc.arg('workflow_id')::text AS id
    UNION ALL
    SELECT c.id FROM workflows c JOIN subtree s ON c.parent_id = s.id
)
UPDATE threads AS t
SET status = sqlc.arg('status')::int,
    completed_at = NOW()
FROM subtree
WHERE t.workflow_id = subtree.id
  AND t.status IN (2, 6);

-- name: ReapOrphanedThreads :execrows
-- Enforce the invariant CascadeTerminalStatusToThreadSubtree asserts from
-- the other direction: a thread whose WORKFLOW is terminal is not running.
-- The thread-status mirror of ReapOrphanedWorkflowDescendants (workflows.sql)
-- -- read its comment block first, this repairs the exact same shape one
-- level over.
--
-- CascadeTerminalStatusToThreadSubtree only runs when the write path that
-- moved a workflow to a terminal status remembers to call it. Every call site
-- that cascades workflow descendants (CancelChat, the reconciler's terminal
-- repair, PauseService's expiry reconciliation, WorkflowStatusActivity's
-- completed/failed/cancelled arms) must call this too, and a forgotten one
-- strands the thread forever: nothing else ever revisits a threads row, and
-- the 288-row measurement in
-- docs/incidents/2026-08-12-spawn-history-cap.md is exactly what that
-- omission looks like at scale -- 174 completed, 64 cancelled, 50 failed
-- workflows, each with a thread still reporting running.
--
-- Fails CLOSED in the same direction as ListStrandedSpawnToolCalls: a thread
-- whose workflow is still LIVE — active, or stopped only because it is paused
-- — is never touched, because inventing a terminal status for a thread backing
-- live work is unrecoverable: it would falsely mark an in-flight agent's
-- mailbox resolvable and its conversation finished. A thread that stays
-- stranded a little longer is recoverable; one wrongly closed is not.
--
-- The thread inherits the workflow's stop REASON, translated into the thread
-- status vocabulary (threads keep a flat status; they have no
-- pending/active distinction to make). This is the SQL twin of
-- core.ThreadStatusForStopReason — a repaired cancel must not read as a
-- repaired success.
UPDATE threads AS t
SET status = CASE w.stop_reason
        WHEN 1 THEN 3  -- COMPLETED -> thread completed
        WHEN 2 THEN 4  -- FAILED    -> thread failed
        WHEN 4 THEN 5  -- CANCELLED -> thread cancelled
    END,
    completed_at = NOW()
FROM workflows w
WHERE t.workflow_id = w.id
  AND w.state = 3 AND w.stop_reason IN (1, 2, 4)
  AND t.status IN (2, 6);

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
