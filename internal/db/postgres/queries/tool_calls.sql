-- name: UpsertToolCall :exec
-- Activities that create/update a tool call retry on failure, so the write
-- must be idempotent: a retry re-sending the same id updates the row in
-- place instead of erroring on the primary key.
INSERT INTO tool_calls (
    id, chat_id, thread_id, message_id, tool_name, input, status,
    error_message, child_workflow_id, background_process_id,
    requested_at, started_at, completed_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
)
ON CONFLICT (id) DO UPDATE SET
    thread_id = EXCLUDED.thread_id,
    message_id = EXCLUDED.message_id,
    tool_name = EXCLUDED.tool_name,
    input = EXCLUDED.input,
    status = EXCLUDED.status,
    error_message = EXCLUDED.error_message,
    child_workflow_id = EXCLUDED.child_workflow_id,
    background_process_id = EXCLUDED.background_process_id,
    started_at = EXCLUDED.started_at,
    completed_at = EXCLUDED.completed_at,
    updated_at = EXCLUDED.updated_at;

-- name: UpsertToolCallResult :exec
INSERT INTO tool_call_results (
    tool_call_id, message_id, content, is_error, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (tool_call_id) DO UPDATE SET
    message_id = EXCLUDED.message_id,
    content = EXCLUDED.content,
    is_error = EXCLUDED.is_error,
    updated_at = EXCLUDED.updated_at;

-- name: GetToolCall :one
SELECT * FROM tool_calls WHERE id = $1;

-- name: GetToolCallResult :one
SELECT * FROM tool_call_results WHERE tool_call_id = $1;

-- name: ListToolCallsByChat :many
SELECT * FROM tool_calls
WHERE chat_id = $1
ORDER BY requested_at ASC;

-- name: ListToolCallsByMessageIDs :many
SELECT * FROM tool_calls
WHERE message_id IN (sqlc.slice('message_ids'))
ORDER BY message_id, requested_at ASC;

-- name: ListToolCallResultsByMessageIDs :many
SELECT * FROM tool_call_results
WHERE message_id IN (sqlc.slice('message_ids'))
ORDER BY message_id, created_at ASC;

-- name: ListStrandedSpawnToolCalls :many
-- Spawn tool calls whose child workflow is terminal but which never received a
-- result: the join from a finished sub-agent back to its parent, broken.
--
-- executeSpawnInline writes the child's terminal workflow status and the
-- parent's tool-call result as two separate activities. A worker that dies
-- between them leaves the child correctly recorded as finished and the parent's
-- call stuck at pending (1) / executing (2) forever. Nothing revisits it:
-- Cleanup only runs when a workflow reaches an abnormal terminal path, and it
-- is scoped to the ENDING workflow's own thread — the stranded row belongs to
-- the still-live PARENT's thread, so it is outside every scope that cleans.
-- Observed on real data: a child failed at 22:16:54 (a worker restart is in the
-- logs at 22:08) and its parent's spawn call was still "executing" 49 hours
-- later, with the sub-agent's work silently dropped.
--
-- Anchored on the same durable evidence callIsStillLive already trusts —
-- tool_calls.child_workflow_id joined to the child's status — so a row
-- returned here is dead by exactly the rule cleanup applies one call at a time.
-- Fails CLOSED in the same direction: a child that is still LIVE — active, or
-- stopped only because it is paused — is never returned, because fabricating a
-- failure for a live spawn writes a lie into conversation history that no
-- later pass can distinguish from a real one. A missing result is recoverable;
-- an invented one is not.
SELECT tc.* FROM tool_calls tc
JOIN workflows w ON w.id = tc.child_workflow_id
WHERE tc.tool_name = 'spawn'
  AND tc.status IN (1, 2)
  AND w.state = 3 AND w.stop_reason <> 3
  AND NOT EXISTS (
      SELECT 1 FROM tool_call_results r WHERE r.tool_call_id = tc.id
  )
ORDER BY tc.requested_at ASC;

-- name: ListStrandedBackgroundSpawnToolCalls :many
-- The async-spawn counterpart to ListStrandedSpawnToolCalls above (spec:
-- async-spawn-and-agent-messaging.md, §7.1). A background=true spawn
-- (dispatchSpawnBackground/workflow.go) writes tool_calls.status = 6
-- (backgrounded) AND its tool_call_results row AT DISPATCH TIME, before the
-- child has done any work -- so neither "status IN (1,2)" nor
-- "NOT EXISTS(tool_call_results)" can ever match one, no matter how long its
-- child has been finished with nothing delivered to the parent. The durable
-- anchor moves to the mailbox: the detached goroutine that runs the child's
-- turns reports its outcome by enqueueing a terminal-kind (2=completion,
-- 3=cancelled, 4=failed) agent_messages row addressed to the parent thread,
-- keyed by tool_call_id (see EnqueueAgentMessageIfAbsent). A worker that
-- dies between the child reaching a terminal workflow status and that
-- enqueue landing leaves the exact same silent-drop shape the sync repair
-- exists for, just with the join anchor moved from tool_call_results to
-- agent_messages.
--
-- Same fail-closed discipline as the sync version: a child that is still LIVE
-- — active, or stopped only because it is paused — is never returned, because
-- fabricating a completion for a live spawn writes a lie into the parent's
-- mailbox that no later pass can distinguish from a real one. A missing
-- report is recoverable; an invented one is not.
SELECT tc.id AS tool_call_id,
       tc.chat_id,
       tc.thread_id AS parent_thread_id,
       w.thread AS child_thread_id,
       w.state AS workflow_state,
       w.stop_reason AS workflow_stop_reason
FROM tool_calls tc
JOIN workflows w ON w.id = tc.child_workflow_id
WHERE tc.tool_name = 'spawn'
  AND tc.status = 6
  AND w.state = 3 AND w.stop_reason <> 3
  AND NOT EXISTS (
      SELECT 1 FROM agent_messages m
      WHERE m.tool_call_id = tc.id AND m.kind IN (2, 3, 4)
  )
ORDER BY tc.requested_at ASC;

-- name: ListSpawnChildrenForThread :many
-- Every spawn call a thread has issued, joined to the child's live workflow
-- and thread rows. tool_calls.thread_id is the PARENT's thread — see
-- ListStrandedSpawnToolCalls above for the same join anchor — so this needs
-- no new column. LEFT JOIN throughout: a child_workflow_id can be set before
-- CreateWorkflowWithThread's activity has landed the workflow/thread rows
-- (a narrow window right after dispatch), and a caller listing mid-race
-- should see the call as pending rather than get an empty result.
--
-- w.thread (not tc.child_workflow_id) is the join key into threads: for a
-- RESUMED spawn (agent_id set), child_workflow_id names the workflow
-- executing THIS call, which is a fresh id each resumption, while w.thread is
-- the pre-existing thread being resumed — the one spawn_status/spawn_send
-- must address. CreateWorkflowWithThread always sets Workflow.Thread to the
-- child thread id, new or resumed, so this join is correct in both cases.
-- workflows.created_at and completed_at are NOT NULL in their own table, so
-- sqlc types them as plain (non-nullable) Go fields regardless of this LEFT
-- JOIN -- scanning the NULL a missing/not-yet-created child would produce
-- crashes at runtime. tc.requested_at already carries "when was this spawn
-- issued", so workflow_created_at is omitted rather than selected and left
-- to crash the first time a caller lists mid-dispatch-race.
SELECT
    tc.id AS tool_call_id,
    tc.status AS tool_call_status,
    tc.input AS tool_input,
    tc.requested_at,
    tc.completed_at,
    w.thread AS child_thread_id,
    w.state AS workflow_state,
    w.stop_reason AS workflow_stop_reason,
    w.completed_at AS workflow_completed_at,
    t.title AS thread_title
FROM tool_calls tc
LEFT JOIN workflows w ON w.id = tc.child_workflow_id
LEFT JOIN threads t ON t.id = w.thread
WHERE tc.tool_name = 'spawn'
  AND tc.thread_id = $1
ORDER BY tc.requested_at ASC;
