-- The cross-cutting run list (RunService.ListRuns, list_runs tool).
--
-- A run is a chat row (chat id == root workflow id), so the list is
-- chats_with_activity joined to its root workflow and, for automation-fired
-- runs, to the trigger. display_state is the one status vocabulary the UI
-- shows, derived once in chats_with_activity.display_state so a filter on it
-- and the label a row carries cannot disagree:
--   1 queued       no root workflow row yet, or PENDING
--   2 running      ACTIVE
--   3 needs-input  ACTIVE and activity = awaiting input (2)
--   8 waiting-for-machine  ACTIVE and activity = waiting for daemon (5)
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
        COALESCE(c.display_state, 1)::integer AS display_state,
        c.state AS chat_state,
        COALESCE(pe.parent_chat_id, '')::text AS parent_chat_id,
        pc.title AS parent_chat_title
    FROM chats_with_activity c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    -- The parent chat of an agent-started run lives only in the launch event's
    -- payload. Looked up for agent.start_run chats alone, by chat id
    -- (idx_trigger_events_chat), so every other row pays nothing.
    LEFT JOIN LATERAL (
        SELECT te.payload->>'parent_chat_id' AS parent_chat_id
        FROM trigger_events te
        WHERE te.chat_id = c.id AND c.launch_kind = 'agent.start_run'
        ORDER BY te.created_at ASC, te.id ASC
        LIMIT 1
    ) pe ON true
    -- Owner-scoped: a parent that is not the caller's contributes no title.
    LEFT JOIN chats pc ON pc.id = pe.parent_chat_id AND pc.user_id = c.user_id
    WHERE c.user_id = sqlc.arg('user_id')
      -- The reverse direction, driven from idx_trigger_events_parent_chat.
      AND (sqlc.narg('parent_chat_id')::text IS NULL OR c.id IN (
            SELECT te.chat_id FROM trigger_events te
            WHERE te.kind = 'agent.start_run'
              AND te.payload->>'parent_chat_id' = sqlc.narg('parent_chat_id')::text
              AND te.chat_id IS NOT NULL))
) r
WHERE
    (sqlc.arg('include_archived')::boolean OR r.chat_state IS DISTINCT FROM 3)
    AND (sqlc.narg('project_id')::text IS NULL OR r.project_id = sqlc.narg('project_id')::text)
    AND (cardinality(sqlc.arg('workflows')::text[]) = 0 OR r.workflow_name = ANY(sqlc.arg('workflows')::text[]))
    AND (sqlc.narg('trigger_id')::text IS NULL OR r.trigger_id = sqlc.narg('trigger_id')::text)
    -- A chat with no launch event is an interactive start, as ListChats treats it.
    -- With no kind named, builder test runs are left out: they are scratch runs
    -- from the workflow builder, not part of the user's real history. Asking for
    -- 'builder.test' explicitly (the Runs "Tests" filter) brings them back.
    AND (CASE WHEN cardinality(sqlc.arg('launch_kinds')::text[]) = 0
              THEN COALESCE(r.launch_kind, 'chat.start') <> 'builder.test'
              ELSE COALESCE(r.launch_kind, 'chat.start') = ANY(sqlc.arg('launch_kinds')::text[]) END)
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
        COALESCE(c.display_state, 1)::integer AS display_state,
        c.state AS chat_state,
        COALESCE(pe.parent_chat_id, '')::text AS parent_chat_id,
        pc.title AS parent_chat_title
    FROM chats_with_activity c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    -- The parent chat of an agent-started run lives only in the launch event's
    -- payload. Looked up for agent.start_run chats alone, by chat id
    -- (idx_trigger_events_chat), so every other row pays nothing.
    LEFT JOIN LATERAL (
        SELECT te.payload->>'parent_chat_id' AS parent_chat_id
        FROM trigger_events te
        WHERE te.chat_id = c.id AND c.launch_kind = 'agent.start_run'
        ORDER BY te.created_at ASC, te.id ASC
        LIMIT 1
    ) pe ON true
    -- Owner-scoped: a parent that is not the caller's contributes no title.
    LEFT JOIN chats pc ON pc.id = pe.parent_chat_id AND pc.user_id = c.user_id
    WHERE c.user_id = sqlc.arg('user_id')
      -- The reverse direction, driven from idx_trigger_events_parent_chat.
      AND (sqlc.narg('parent_chat_id')::text IS NULL OR c.id IN (
            SELECT te.chat_id FROM trigger_events te
            WHERE te.kind = 'agent.start_run'
              AND te.payload->>'parent_chat_id' = sqlc.narg('parent_chat_id')::text
              AND te.chat_id IS NOT NULL))
) r
WHERE
    r.chat_state IS DISTINCT FROM 3
    AND r.launch_kind IS DISTINCT FROM 'builder.test'
    AND r.workflow_name <> ''
    AND (sqlc.narg('project_id')::text IS NULL OR r.project_id = sqlc.narg('project_id')::text)
    AND (cardinality(sqlc.arg('workflows')::text[]) = 0 OR r.workflow_name = ANY(sqlc.arg('workflows')::text[]))
ORDER BY r.workflow_name, r.created_at DESC, r.chat_id DESC;
