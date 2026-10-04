-- name: CreateTrigger :exec
INSERT INTO triggers (
    id, user_id, project_id, worktree_id, name, kind, enabled,
    workflow, presets, params, message, config, created_at, updated_at,
    daemon_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
);

-- name: GetTrigger :one
SELECT * FROM triggers WHERE id = $1;

-- name: ListTriggers :many
-- An empty user_id lists every user's triggers, which only the schedule
-- syncer's startup reconciliation does. project_id narrows to one project.
SELECT * FROM triggers
WHERE
    (sqlc.arg('user_id')::text = '' OR user_id = sqlc.arg('user_id')::text)
    AND (sqlc.narg('project_id')::text IS NULL OR project_id = sqlc.narg('project_id')::text)
ORDER BY created_at DESC, id;

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

-- name: ListTriggerEvents :many
-- Newest first, matching idx_trigger_events_trigger_occurred so this is an
-- ordered index scan. id breaks ties: two fires can share an occurred_at.
SELECT * FROM trigger_events
WHERE trigger_id = $1
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg('limit');

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
