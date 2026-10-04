-- The cross-cutting run list (RunService.ListRuns, list_runs tool).
--
-- A run is a chat row (chat id == root workflow id), so the list is
-- chats_with_activity joined to its root workflow and, for automation-fired
-- runs, to the trigger. display_state is the one status vocabulary the UI
-- shows, derived here so a filter on it and the label a row carries cannot
-- disagree:
--   1 queued       no root workflow row yet, or PENDING
--   2 running      ACTIVE
--   3 needs-input  ACTIVE and activity = awaiting input (2)
--   4 paused       STOPPED, stop_reason PAUSED (3)
--   5 completed    STOPPED, stop_reason COMPLETED (1)
--   6 failed       STOPPED, stop_reason FAILED (2)
--   7 cancelled    STOPPED, stop_reason CANCELLED (4)
-- Every filter argument is nullable or an array: an unset one must not narrow.

-- name: ListRuns :many
SELECT * FROM (
    SELECT
        c.id AS chat_id,
        COALESCE(c.workflow_id, c.id)::text AS run_id,
        c.title,
        c.project_id,
        c.created_at,
        c.last_active,
        rw.completed_at AS completed_at,
        COALESCE(rw.workflow_name, c.workflow_name, '')::text AS workflow_name,
        rw.outcome AS outcome,
        rw.state AS root_state,
        rw.stop_reason AS root_stop_reason,
        c.activity,
        c.launch_kind,
        c.trigger_id,
        t.name AS trigger_name,
        c.active_daemon_id,
        CASE
            WHEN rw.state IS NULL OR rw.state = 1 THEN 1
            WHEN rw.state = 2 AND c.activity = 2 THEN 3
            WHEN rw.state = 2 THEN 2
            WHEN rw.state = 3 AND rw.stop_reason = 3 THEN 4
            WHEN rw.state = 3 AND rw.stop_reason = 1 THEN 5
            WHEN rw.state = 3 AND rw.stop_reason = 2 THEN 6
            WHEN rw.state = 3 AND rw.stop_reason = 4 THEN 7
            ELSE 0
        END::integer AS display_state,
        c.state AS chat_state
    FROM chats_with_activity c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    WHERE c.user_id = sqlc.arg('user_id')
) r
WHERE
    (sqlc.arg('include_archived')::boolean OR r.chat_state IS DISTINCT FROM 3)
    AND (sqlc.narg('project_id')::text IS NULL OR r.project_id = sqlc.narg('project_id')::text)
    AND (cardinality(sqlc.arg('workflows')::text[]) = 0 OR r.workflow_name = ANY(sqlc.arg('workflows')::text[]))
    AND (sqlc.narg('trigger_id')::text IS NULL OR r.trigger_id = sqlc.narg('trigger_id')::text)
    -- A chat with no launch event is an interactive start, as ListChats treats it.
    AND (cardinality(sqlc.arg('launch_kinds')::text[]) = 0
         OR COALESCE(r.launch_kind, 'chat.start') = ANY(sqlc.arg('launch_kinds')::text[]))
    AND (cardinality(sqlc.arg('display_states')::integer[]) = 0 OR r.display_state = ANY(sqlc.arg('display_states')::integer[]))
    AND (sqlc.narg('started_after')::timestamptz IS NULL OR r.created_at >= sqlc.narg('started_after')::timestamptz)
    AND (sqlc.narg('started_before')::timestamptz IS NULL OR r.created_at < sqlc.narg('started_before')::timestamptz)
    AND (sqlc.narg('query')::text IS NULL OR position(lower(sqlc.narg('query')::text) in lower(r.title)) > 0)
    AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
         OR (r.created_at, r.chat_id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::text))
ORDER BY
    CASE WHEN sqlc.arg('by_last_active')::boolean THEN r.last_active END DESC NULLS LAST,
    r.created_at DESC, r.chat_id DESC
LIMIT sqlc.arg('row_limit');

-- name: LastRunPerWorkflow :many
-- Newest run of each workflow name. Same columns and display_state as ListRuns.
SELECT DISTINCT ON (r.workflow_name) r.* FROM (
    SELECT
        c.id AS chat_id,
        COALESCE(c.workflow_id, c.id)::text AS run_id,
        c.title,
        c.project_id,
        c.created_at,
        c.last_active,
        rw.completed_at AS completed_at,
        COALESCE(rw.workflow_name, c.workflow_name, '')::text AS workflow_name,
        rw.outcome AS outcome,
        rw.state AS root_state,
        rw.stop_reason AS root_stop_reason,
        c.activity,
        c.launch_kind,
        c.trigger_id,
        t.name AS trigger_name,
        c.active_daemon_id,
        CASE
            WHEN rw.state IS NULL OR rw.state = 1 THEN 1
            WHEN rw.state = 2 AND c.activity = 2 THEN 3
            WHEN rw.state = 2 THEN 2
            WHEN rw.state = 3 AND rw.stop_reason = 3 THEN 4
            WHEN rw.state = 3 AND rw.stop_reason = 1 THEN 5
            WHEN rw.state = 3 AND rw.stop_reason = 2 THEN 6
            WHEN rw.state = 3 AND rw.stop_reason = 4 THEN 7
            ELSE 0
        END::integer AS display_state,
        c.state AS chat_state
    FROM chats_with_activity c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    WHERE c.user_id = sqlc.arg('user_id')
) r
WHERE
    r.chat_state IS DISTINCT FROM 3
    AND r.workflow_name <> ''
    AND (sqlc.narg('project_id')::text IS NULL OR r.project_id = sqlc.narg('project_id')::text)
    AND (cardinality(sqlc.arg('workflows')::text[]) = 0 OR r.workflow_name = ANY(sqlc.arg('workflows')::text[]))
ORDER BY r.workflow_name, r.created_at DESC, r.chat_id DESC;
