-- The write ledger. See the migration 20261014055046_integration_writes.sql
-- for the model: an intent row is written before an integration action's
-- request, completed after, and read when an event arrives to say whether
-- Reliant caused it.

-- name: CreateIntegrationWrite :exec
INSERT INTO integration_writes (
    id, user_id, chat_id, workflow, trigger_id, trigger_name, node_id, tool_call_id,
    integration, action, connection_id, actor, scope, target, correlation,
    object_id, object_url, status, ambiguous, error, created_at, completed_at
) VALUES (
    sqlc.arg('id')::text, sqlc.arg('user_id')::text, sqlc.arg('chat_id')::text, sqlc.arg('workflow')::text,
    sqlc.arg('trigger_id')::text, sqlc.arg('trigger_name')::text, sqlc.arg('node_id')::text, sqlc.arg('tool_call_id')::text,
    sqlc.arg('integration')::text, sqlc.arg('action')::text, sqlc.arg('connection_id')::text, sqlc.arg('actor')::text,
    sqlc.arg('scope')::text, sqlc.arg('target')::text, sqlc.arg('correlation')::jsonb,
    sqlc.arg('object_id')::text, sqlc.arg('object_url')::text, sqlc.arg('status')::text,
    sqlc.arg('ambiguous')::boolean, sqlc.arg('error')::text, sqlc.arg('created_at')::timestamptz,
    sqlc.narg('completed_at')::timestamptz
);

-- name: CompleteIntegrationWrite :execrows
-- Settles an intent row. Only an intent row settles: a ledger entry is never
-- rewritten after the fact, so a duplicate completion is a no-op.
UPDATE integration_writes SET
    status = sqlc.arg('status')::text,
    target = CASE WHEN sqlc.arg('target')::text <> '' THEN sqlc.arg('target')::text ELSE target END,
    object_id = sqlc.arg('object_id')::text,
    object_url = sqlc.arg('object_url')::text,
    connection_id = sqlc.arg('connection_id')::text,
    ambiguous = sqlc.arg('ambiguous')::boolean,
    error = sqlc.arg('error')::text,
    completed_at = sqlc.arg('completed_at')::timestamptz
WHERE id = sqlc.arg('id')::text AND status = 'intent';

-- name: GetIntegrationWriteByObject :one
-- The write that created (or is) one object, newest first. Served by
-- idx_integration_writes_object.
SELECT * FROM integration_writes
WHERE integration = sqlc.arg('integration')::text
  AND scope = sqlc.arg('scope')::text
  AND object_id = sqlc.arg('object_id')::text
  AND correlation ->> 'object_kind' = sqlc.arg('object_kind')::text
  AND status = 'done'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListRecentIntegrationWrites :many
-- Writes into one scope since a time, newest first, that could still be the
-- cause of an arriving event: pending, completed, or failed with an unknown
-- outcome. A definite rejection (a 4xx) changed nothing and so caused nothing.
-- Served by idx_integration_writes_scope_recent.
SELECT * FROM integration_writes
WHERE integration = sqlc.arg('integration')::text
  AND scope = sqlc.arg('scope')::text
  AND created_at >= sqlc.arg('since')::timestamptz
  AND (status <> 'failed' OR ambiguous)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit');

-- name: PruneIntegrationWrites :execrows
DELETE FROM integration_writes WHERE created_at < sqlc.arg('before')::timestamptz;
