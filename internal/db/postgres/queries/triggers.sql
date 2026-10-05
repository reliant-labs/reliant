-- name: CreateTrigger :exec
INSERT INTO triggers (
    id, user_id, project_id, worktree_id, name, kind, enabled,
    workflow, presets, params, message, config, created_at, updated_at,
    daemon_id, notify_on_complete, filter, connection_id, workflow_trigger
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
);

-- name: SetTriggerWebhookTokenHash :execrows
-- The token hash has its own write path: no definition update can touch it,
-- and rotating it is one statement.
UPDATE triggers SET webhook_token_hash = $1, updated_at = NOW() WHERE id = $2;

-- name: SetTriggerWebhookSecret :execrows
-- NULL clears the HMAC secret. Sealed by the caller under the owner's tenant.
UPDATE triggers SET webhook_secret_sealed = $1, updated_at = NOW() WHERE id = $2;

-- name: GetTriggerWebhookCredentials :one
-- Only the webhook receiver reads these. They are never rendered.
SELECT webhook_token_hash, webhook_secret_sealed FROM triggers WHERE id = $1;

-- name: ListIntegrationTriggers :many
-- Every ENABLED integration trigger for one integration, with its
-- connection's routing identity. The inner join drops triggers whose
-- connection is gone, revoked or belongs to someone else: an event can only
-- reach a trigger through its own owner's live connection. Status is returned
-- rather than filtered so a caller can tell needs_reauth apart.
SELECT
    sqlc.embed(t),
    c.external_account_id AS connection_account,
    c.status AS connection_status
FROM triggers t
JOIN connections c
    ON c.id = t.connection_id
    AND c.user_id = t.user_id
    AND c.owner_kind = 'user'
    AND c.deleted_at IS NULL
WHERE t.kind = 'integration'
    AND t.enabled
    AND t.config ->> 'integration' = sqlc.arg('integration')::text
ORDER BY t.id;

-- name: ListStalePendingTriggerEvents :many
-- Inbound events recorded but not yet settled, older than the cutoff: the
-- fire workflow for them was never started (the receiver crashed between the
-- insert and the start) or died. The redriver restarts their fires.
SELECT * FROM trigger_events
WHERE outcome = 'pending' AND created_at < sqlc.arg('older_than')::timestamptz
ORDER BY created_at
LIMIT sqlc.arg('row_limit');

-- name: ClaimPendingTriggerEvent :execrows
-- The launcher adopts an inbound event the receiver recorded: the row moves
-- from pending to launched (chat attached later in the same transaction) and
-- takes the launch's start record as its payload. The outcome predicate is
-- what makes two concurrent fires of one event launch it once: the loser
-- updates zero rows.
UPDATE trigger_events SET outcome = 'launched', outcome_detail = '', payload = $1
WHERE id = $2 AND outcome = 'pending';

-- name: SettlePendingTriggerEvent :execrows
-- Records the verdict on an inbound event that did NOT launch. Conditional on
-- pending, so it can never overwrite a launch that won a race.
UPDATE trigger_events SET outcome = $1, outcome_detail = $2
WHERE id = $3 AND outcome = 'pending';

-- name: GetTriggerRegistration :one
SELECT * FROM trigger_registrations WHERE trigger_id = $1;

-- name: UpsertTriggerRegistration :exec
INSERT INTO trigger_registrations (
    trigger_id, provider, registration_id, cursor, last_polled_at, status, status_detail, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, NOW(), NOW()
)
ON CONFLICT (trigger_id) DO UPDATE SET
    provider = EXCLUDED.provider,
    registration_id = EXCLUDED.registration_id,
    cursor = EXCLUDED.cursor,
    last_polled_at = EXCLUDED.last_polled_at,
    status = EXCLUDED.status,
    status_detail = EXCLUDED.status_detail,
    updated_at = NOW();

-- name: DeleteTriggerRegistration :exec
DELETE FROM trigger_registrations WHERE trigger_id = $1;

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
    daemon_id = $11,
    notify_on_complete = $12,
    filter = $13,
    connection_id = $14,
    workflow_trigger = $15
WHERE id = $16;

-- name: DeleteTrigger :exec
DELETE FROM triggers WHERE id = $1;

-- name: SetTriggerEnabled :execrows
UPDATE triggers SET enabled = $1, updated_at = NOW() WHERE id = $2;

-- name: SetTriggerProjection :execrows
-- Refreshes an activation's projection of its workflow's declaration: the
-- config and filter routing and the schedule syncer read. The kind is not
-- here: an activation whose declaration changed kind is broken (its
-- connection or webhook token belongs to the old kind), never re-projected.
UPDATE triggers SET
    config = $1,
    filter = $2,
    updated_at = NOW()
WHERE id = $3 AND workflow_trigger IS NOT NULL;

-- name: ListWorkflowTriggerActivations :many
-- One user's activations of a workflow's declared triggers. The workflow is
-- matched as stored on the row (the slug or builtin:// ref the activation
-- named).
SELECT * FROM triggers
WHERE user_id = $1 AND workflow = $2 AND workflow_trigger IS NOT NULL
ORDER BY created_at, id;

-- name: ListAllWorkflowTriggerActivations :many
-- Every activation of a declared trigger, for the periodic reconcile.
SELECT * FROM triggers WHERE workflow_trigger IS NOT NULL ORDER BY id;

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
    COALESCE(c.display_state, 1)::integer AS run_display_state
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
    COALESCE(c.display_state, 1)::integer AS run_display_state
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

-- name: ListFiringsSinceLastSuccess :many
-- Every firing of each named trigger after its newest SUCCESS (a launched
-- firing whose run completed), newest first, capped at per_trigger rows. A
-- trigger with no success returns its whole history up to the cap. This is
-- the failure episode: Go reads its length and its oldest failure from it, so
-- the episode is not truncated at the 10-firing health window. Same run
-- columns and display-state table as ListRecentTriggerFirings.
WITH last_success AS (
    SELECT DISTINCT ON (e.trigger_id) e.trigger_id, e.occurred_at, e.id
    FROM trigger_events e
    JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
    WHERE e.user_id = sqlc.arg('user_id')::text
        AND e.trigger_id = ANY(sqlc.arg('trigger_ids')::text[])
        AND e.outcome = 'launched'
        AND c.display_state = 5
    ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC
), ranked AS (
    SELECT
        e.id,
        row_number() OVER (PARTITION BY e.trigger_id ORDER BY e.occurred_at DESC, e.id DESC) AS rn
    FROM trigger_events e
    LEFT JOIN last_success s ON s.trigger_id = e.trigger_id
    WHERE e.user_id = sqlc.arg('user_id')::text
        AND e.trigger_id = ANY(sqlc.arg('trigger_ids')::text[])
        AND (s.id IS NULL OR (e.occurred_at, e.id) > (s.occurred_at, s.id))
)
SELECT
    sqlc.embed(e),
    c.id AS run_chat_id,
    c.title AS run_title,
    rw.state AS run_root_state,
    rw.stop_reason AS run_root_stop_reason,
    COALESCE(c.display_state, 1)::integer AS run_display_state
FROM ranked r
JOIN trigger_events e ON e.id = r.id
LEFT JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
LEFT JOIN workflows rw ON rw.id = c.workflow_id
WHERE r.rn <= sqlc.arg('per_trigger')::integer
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC;
