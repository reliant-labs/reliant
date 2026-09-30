-- name: CreateStepExecution :one
INSERT INTO step_executions (
    id, workflow_id, step_id, activity_name,
    output_json, exit_code, success, duration_ms,
    loop_node_id, loop_iteration, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetStepExecution :one
SELECT * FROM step_executions WHERE id = $1;

-- name: GetStepExecutions :many
-- Get all executions of a specific step in a workflow (for CEL history queries)
SELECT * FROM step_executions
WHERE workflow_id = $1 AND step_id = $2
ORDER BY created_at ASC;

-- name: GetAllStepExecutionsForWorkflow :many
-- Get all step executions in a workflow (for full history reconstruction)
SELECT * FROM step_executions
WHERE workflow_id = $1
ORDER BY created_at ASC;

-- name: DeleteStepExecutionsForWorkflow :exec
DELETE FROM step_executions WHERE workflow_id = $1;

-- name: GetStepExecutionsForChat :many
-- Every step of every workflow of one chat, in one round trip, for
-- ChatService/GetWorkflowExecutions.
--
-- This replaces a loop that ran GetAllStepExecutionsForWorkflow once per
-- workflow: 83 queries for the worst real chat, each a SELECT * that dragged
-- output_json along. Measured on the dev database, chat abe58f03-…: the 83
-- serial round trips took 2.61s of a pool with 8 connections, so a single
-- sidebar refetch could monopolize it and stall every other RPC.
--
-- output_json is read for almost no row, and that is the whole point. It is why
-- this table is 905 MB against a 361 MB heap — TOASTed out of line, so touching
-- it costs extra reads per row — and the two things clients need from it are
-- obtained without touching it for the rows that dominate:
--
--   * the saved message id of a "-save" step arrives as saved_message_id, a
--     generated column (migration 20260929162546). Extracting it from
--     output_json here instead measured ~520ms against ~42ms, essentially all
--     of it detoasting 37 MB of JSON to recover 16k UUIDs.
--   * the rich context ActivityIndicator and the workflow viewer's Output panel
--     render is still selected in full, but ONLY for user-facing activities.
--     That is what makes it affordable: of this chat's 32,023 steps, 32,013 are
--     internal plumbing (SaveMessage / CallLLM / ExecuteTools), and the ten
--     that remain total 23 kB. Measured cost of including them: ~45ms vs ~42ms
--     for selecting no output_json at all.
--
-- The internal-activity list is a PARAMETER, not a literal, so the set lives in
-- exactly one Go place (workflowmodel.InternalActivities) and cannot drift from
-- the frontend's INTERNAL_ACTIVITIES, which decides the same question for the
-- same reason in activityIndicators.ts. A row whose activity is in the list is
-- one the UI never renders output for; if the two lists disagree, the symptom is
-- an activity indicator with no context rather than an error, which is the kind
-- of divergence nothing would catch.
--
-- ORDER BY (workflow_id, created_at) matches idx_step_executions_chat_read, so
-- the sort proceeds incrementally over already-ordered groups instead of
-- spilling to disk, and every scalar column below is covered by that index.
SELECT se.id, se.workflow_id, se.step_id, se.activity_name,
       se.exit_code, se.success, se.duration_ms,
       se.loop_node_id, se.loop_iteration, se.created_at,
       se.saved_message_id,
       -- Empty string, not NULL, for the rows whose output is withheld: sqlc
       -- infers a bare CASE as interface{} and a cast one as a non-nullable
       -- string, so COALESCE makes the Go type honest instead of fighting the
       -- inference. Empty and absent are the same thing here — output_json is
       -- written by json.Marshal, so a real value is never the empty string.
       --
       -- Spelling this as a LEFT JOIN to the row itself types cleanly but costs
       -- a primary-key lookup per row: measured on the dev database, 32,494
       -- extra index scans, 146k buffers against 16k, ~200ms against ~45ms.
       COALESCE(
           CASE WHEN se.activity_name <> ALL(sqlc.arg('internal_activities')::text[])
                THEN se.output_json END,
           ''
       )::text AS output_json
FROM step_executions se
JOIN workflows w ON w.id = se.workflow_id
WHERE w.chat_id = sqlc.arg('chat_id')
ORDER BY se.workflow_id, se.created_at ASC;
