-- name: CreateTrigger :exec
INSERT INTO triggers (
    id, user_id, project_id, worktree_id, name, kind, enabled,
    workflow, presets, params, message, config, created_at, updated_at,
    daemon_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
);

-- name: GetTrigger :one
-- Joins the display names so a trigger can be shown without a second lookup.
-- LEFT JOINs: a daemon id is not a foreign key, and an absent name must not
-- hide the trigger.
SELECT
    sqlc.embed(t),
    COALESCE(p.name, '')::text AS project_name,
    COALESCE(d.hostname, '')::text AS daemon_name
FROM triggers t
LEFT JOIN projects p ON p.id = t.project_id
LEFT JOIN daemons d ON d.id = t.daemon_id AND d.user_id = t.user_id
WHERE t.id = $1;

-- name: ListTriggers :many
-- Always scoped to one user; project_id narrows further. The unscoped listing
-- is ListAllTriggers, a separate query so "no user" can never be reached by
-- passing an empty string.
SELECT
    sqlc.embed(t),
    COALESCE(p.name, '')::text AS project_name,
    COALESCE(d.hostname, '')::text AS daemon_name
FROM triggers t
LEFT JOIN projects p ON p.id = t.project_id
LEFT JOIN daemons d ON d.id = t.daemon_id AND d.user_id = t.user_id
WHERE t.user_id = sqlc.arg('user_id')::text
    AND (sqlc.narg('project_id')::text IS NULL OR t.project_id = sqlc.narg('project_id')::text)
ORDER BY t.created_at DESC, t.id;

-- name: ListAllTriggers :many
-- Every user's triggers. Only the schedule syncer's reconciliation calls this.
SELECT * FROM triggers ORDER BY created_at DESC, id;

-- name: LockTrigger :one
-- Row lock on the trigger for the rest of the transaction. Serializes the
-- overlap check with the launch it guards across concurrent fires.
SELECT id FROM triggers WHERE id = $1 FOR UPDATE;

-- name: UpdateTrigger :execrows
-- Identity columns (id, user_id, kind) are not updatable: changing the owner
-- would silently re-point which identity the run executes as, and changing
-- the kind would leave Config describing a source that no longer applies.
UPDATE triggers SET
    project_id = $1,
    worktree_id = $2,
    name = $3,
    enabled = $4,
    workflow = $5,
    presets = $6,
    params = $7,
    message = $8,
    config = $9,
    updated_at = $10,
    daemon_id = $11
WHERE id = $12;

-- name: DeleteTrigger :exec
DELETE FROM triggers WHERE id = $1;

-- name: SetTriggerEnabled :execrows
UPDATE triggers SET enabled = $1, updated_at = NOW() WHERE id = $2;

-- name: CreateTriggerEvent :execrows
-- DO NOTHING rather than DO UPDATE: the first row for a (kind, dedupe_key) is
-- the authoritative record of intent, and a retry must not overwrite its
-- outcome. The affected-row count is what tells the caller whether it won the
-- race (1) or a prior firing already exists (0).
INSERT INTO trigger_events (
    id, trigger_id, user_id, kind, dedupe_key, occurred_at,
    payload, outcome, outcome_detail, chat_id, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (kind, dedupe_key) DO NOTHING;

-- name: GetTriggerEventByDedupe :one
SELECT * FROM trigger_events WHERE kind = $1 AND dedupe_key = $2;

-- name: GetTriggerEventByChatID :one
-- The chat's launch event: the earliest event that references it. Served by
-- idx_trigger_events_chat.
SELECT * FROM trigger_events
WHERE chat_id = $1
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: GetTriggerEventByChat :one
-- The event that launched a chat. The oldest wins: a chat is launched once, and
-- a later row naming it can only be a replay. Served by idx_trigger_events_chat.
SELECT * FROM trigger_events
WHERE kind = $1 AND chat_id = $2
ORDER BY occurred_at, id
LIMIT 1;

-- name: CountLiveLaunchedRuns :one
-- Chats the user launched through this kind of event whose ROOT run is still
-- live: pending (1), active (2), or stopped-because-paused (state 3, stop
-- reason 3). The inner joins drop events whose chat was deleted or never got a
-- root workflow, neither of which can be running.
SELECT count(*) FROM trigger_events e
JOIN chats c ON c.id = e.chat_id
JOIN workflows w ON w.id = c.workflow_id
WHERE e.user_id = sqlc.arg('user_id')
  AND e.kind = sqlc.arg('kind')
  AND e.outcome = 'launched'
  AND (w.state IN (1, 2) OR (w.state = 3 AND w.stop_reason = 3));

-- name: UpdateTriggerEventOutcome :execrows
UPDATE trigger_events SET
    outcome = $1,
    outcome_detail = $2,
    chat_id = $3
WHERE id = $4;

-- name: UpdateTriggerEventPayload :execrows
UPDATE trigger_events SET payload = $1 WHERE id = $2;

-- name: ListTriggerEvents :many
-- Newest first, keyset on (occurred_at, id) so a firing recorded mid-pagination
-- can neither repeat nor be skipped; two fires can share an occurred_at, and id
-- breaks the tie. Served by idx_trigger_events_trigger_occurred_id.
--
-- Each launched firing carries its run. run_display_state is the SAME table as
-- queries/runs.sql (ListRuns) — keep the two in step; a test pins them equal.
-- user_id scopes the rows even though the handler already checked ownership.
SELECT
    sqlc.embed(e),
    c.id AS run_chat_id,
    c.title AS run_title,
    rw.state AS run_root_state,
    rw.stop_reason AS run_root_stop_reason,
    CASE
        WHEN rw.state IS NULL OR rw.state = 1 THEN 1
        WHEN rw.state = 2 AND c.activity = 2 THEN 3
        WHEN rw.state = 2 THEN 2
        WHEN rw.state = 3 AND rw.stop_reason = 3 THEN 4
        WHEN rw.state = 3 AND rw.stop_reason = 1 THEN 5
        WHEN rw.state = 3 AND rw.stop_reason = 2 THEN 6
        WHEN rw.state = 3 AND rw.stop_reason = 4 THEN 7
        ELSE 0
    END::integer AS run_display_state
FROM trigger_events e
LEFT JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
LEFT JOIN workflows rw ON rw.id = c.workflow_id
WHERE e.trigger_id = sqlc.arg('trigger_id')::text
    AND e.user_id = sqlc.arg('user_id')::text
    AND (cardinality(sqlc.arg('outcomes')::text[]) = 0 OR e.outcome = ANY(sqlc.arg('outcomes')::text[]))
    AND (sqlc.narg('cursor_occurred_at')::timestamptz IS NULL
         OR (e.occurred_at, e.id) < (sqlc.narg('cursor_occurred_at')::timestamptz, sqlc.narg('cursor_id')::text))
ORDER BY e.occurred_at DESC, e.id DESC
LIMIT sqlc.arg('row_limit');

-- name: ListRecentTriggerFirings :many
-- The newest per_trigger firings of each named trigger, with their runs, in ONE
-- query. This is what health and last_event are computed from, so listing N
-- triggers costs one extra query rather than N. Same run_display_state table as
-- ListTriggerEvents above. run_display_state is meaningful only when
-- run_chat_id is set.
WITH ranked AS (
    SELECT
        e.id,
        row_number() OVER (PARTITION BY e.trigger_id ORDER BY e.occurred_at DESC, e.id DESC) AS rn
    FROM trigger_events e
    WHERE e.user_id = sqlc.arg('user_id')::text
        AND e.trigger_id = ANY(sqlc.arg('trigger_ids')::text[])
)
SELECT
    sqlc.embed(e),
    c.id AS run_chat_id,
    c.title AS run_title,
    rw.state AS run_root_state,
    rw.stop_reason AS run_root_stop_reason,
    CASE
        WHEN rw.state IS NULL OR rw.state = 1 THEN 1
        WHEN rw.state = 2 AND c.activity = 2 THEN 3
        WHEN rw.state = 2 THEN 2
        WHEN rw.state = 3 AND rw.stop_reason = 3 THEN 4
        WHEN rw.state = 3 AND rw.stop_reason = 1 THEN 5
        WHEN rw.state = 3 AND rw.stop_reason = 2 THEN 6
        WHEN rw.state = 3 AND rw.stop_reason = 4 THEN 7
        ELSE 0
    END::integer AS run_display_state
FROM ranked r
JOIN trigger_events e ON e.id = r.id
LEFT JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
LEFT JOIN workflows rw ON rw.id = c.workflow_id
WHERE r.rn <= sqlc.arg('per_trigger')::integer
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC;

-- name: GetLatestTriggerEvent :one
-- The overlap check ("is this trigger's previous run still going?") asks for
-- the latest 'launched' event; the detail view asks for the latest of any
-- outcome. A NULL outcome means no filter.
SELECT * FROM trigger_events
WHERE
    trigger_id = sqlc.arg('trigger_id')::text
    AND (sqlc.narg('outcome')::text IS NULL OR outcome = sqlc.narg('outcome')::text)
ORDER BY occurred_at DESC, id DESC
LIMIT 1;
