-- run_events: the outbox for workflow-event triggers. See the migration
-- 20261005010407_run_events.sql for the model.

-- name: CreateRunEvent :execrows
-- DO NOTHING on a dedupe_key that already exists: the first row for a
-- transition is the record, and an activity retry must not add a second.
INSERT INTO run_events (
    id, user_id, chat_id, workflow_name, outcome, dedupe_key, payload, occurred_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: GetRunEvent :one
SELECT * FROM run_events WHERE id = $1;

-- name: GetRunEventByDedupe :one
SELECT * FROM run_events WHERE dedupe_key = $1;

-- name: HasEnabledTriggerOfKind :one
-- Whether the user owns at least one enabled trigger of the kind. The emitter
-- asks this before writing an outbox row, so users without workflow-event
-- triggers never pay for one.
SELECT EXISTS (
    SELECT 1 FROM triggers
    WHERE user_id = sqlc.arg('user_id')::text
      AND kind = sqlc.arg('kind')::text
      AND enabled
)::boolean AS has_trigger;

-- name: LockChatForRunEvent :one
-- Row-locks the chat for the rest of the transaction. Serializes the "was the
-- run already blocked?" check across concurrent approval and question
-- creations in the same chat.
SELECT id FROM chats WHERE id = $1 FOR UPDATE;

-- name: CountOtherPendingBlockers :one
-- Pending approvals and questions in the chat other than exclude_id. Zero
-- means the item being created is the one that blocks the run.
SELECT (
    (SELECT count(*) FROM approvals a
     WHERE a.chat_id = sqlc.arg('chat_id')::text AND a.status = 1 AND a.id <> sqlc.arg('exclude_id')::text)
  + (SELECT count(*) FROM questions q
     WHERE q.chat_id = sqlc.arg('chat_id')::text AND q.status = 1 AND q.id <> sqlc.arg('exclude_id')::text)
)::integer AS pending;

-- name: ClaimRunEvents :many
-- Leases up to `max` undispatched events whose lease is free or expired.
-- SKIP LOCKED lets several relays drain the queue without handing the same
-- row to two of them.
UPDATE run_events SET claimed_until = sqlc.arg('claimed_until')::timestamptz
WHERE id IN (
    SELECT r.id FROM run_events r
    WHERE r.dispatched_at IS NULL
      AND (r.claimed_until IS NULL OR r.claimed_until < sqlc.arg('now')::timestamptz)
    ORDER BY r.created_at, r.id
    LIMIT sqlc.arg('max')::integer
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkRunEventDispatched :exec
UPDATE run_events SET dispatched_at = $2 WHERE id = $1;

-- name: DeleteDispatchedRunEventsBefore :execrows
DELETE FROM run_events WHERE dispatched_at IS NOT NULL AND dispatched_at < $1;
