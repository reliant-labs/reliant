-- Access-gated routing for app-level provider events. See the migration
-- 20261005061008_integration_event_access.sql for the model.

-- name: UpsertIntegrationAccess :exec
-- Records one refresh's findings: every (account, resource) the user's own
-- credential can see now, all stamped with the same refreshed_at. The caller
-- then deletes what this refresh did not see (DeleteStaleIntegrationAccess),
-- in the same transaction.
INSERT INTO integration_event_access (
    user_id, integration_id, account_key, resource_key, resource_label, subject_id, refreshed_at
)
SELECT
    sqlc.arg('user_id')::text,
    sqlc.arg('integration_id')::text,
    a.v,
    r.v,
    l.v,
    sqlc.arg('subject_id')::text,
    sqlc.arg('refreshed_at')::timestamptz
-- Three parallel arrays zipped by position (one unnest each, joined on
-- ordinality: sqlc cannot type the multi-argument unnest).
FROM unnest(sqlc.arg('account_keys')::text[]) WITH ORDINALITY AS a(v, i)
JOIN unnest(sqlc.arg('resource_keys')::text[]) WITH ORDINALITY AS r(v, i) ON r.i = a.i
JOIN unnest(sqlc.arg('resource_labels')::text[]) WITH ORDINALITY AS l(v, i) ON l.i = a.i
ON CONFLICT (user_id, integration_id, account_key, resource_key) DO UPDATE SET
    resource_label = EXCLUDED.resource_label,
    subject_id = EXCLUDED.subject_id,
    refreshed_at = EXCLUDED.refreshed_at
-- Monotonic: a slower refresh that started earlier never stamps an older
-- time over a newer refresh's.
WHERE integration_event_access.refreshed_at <= EXCLUDED.refreshed_at;

-- name: DeleteStaleIntegrationAccess :execrows
-- Drops what the user could see before but this refresh (stamped
-- refreshed_at) no longer reports: a repository they lost, an installation
-- they left. Also the whole set when a refresh finds the credential gone.
DELETE FROM integration_event_access
WHERE user_id = sqlc.arg('user_id')::text
    AND integration_id = sqlc.arg('integration_id')::text
    AND refreshed_at < sqlc.arg('refreshed_at')::timestamptz;

-- name: RevokeIntegrationAccess :execrows
-- The provider says access ended. Empty arguments are wildcards, but at least
-- the account or the subject is always given by the caller: an installation
-- removed (account), a repository removed from it (account + resource), a
-- member removed (subject, optionally narrowed to an account).
DELETE FROM integration_event_access
WHERE integration_id = sqlc.arg('integration_id')::text
    AND (sqlc.arg('account_key')::text = '' OR account_key = sqlc.arg('account_key')::text)
    AND (sqlc.arg('resource_key')::text = '' OR resource_key = sqlc.arg('resource_key')::text)
    AND (sqlc.arg('subject_id')::text = '' OR subject_id = sqlc.arg('subject_id')::text);

-- name: ListAccessRoutedTriggers :many
-- Every enabled integration trigger of one integration whose OWNER can see
-- the event's (account, resource) per a snapshot refreshed after
-- fresh_after. A stale snapshot routes nothing: access is only trusted while
-- recently confirmed. A trigger that names a connection must name its own
-- owner's live one; one with no connection (a delegated authority, e.g.
-- GitHub via control-plane) is routed on access alone.
SELECT sqlc.embed(t)
FROM triggers t
JOIN integration_event_access a
    ON a.user_id = t.user_id
    AND a.integration_id = sqlc.arg('integration')::text
    AND a.account_key = sqlc.arg('account_key')::text
    AND a.resource_key = sqlc.arg('resource_key')::text
    AND a.refreshed_at >= sqlc.arg('fresh_after')::timestamptz
LEFT JOIN connections c
    ON c.id = t.connection_id
WHERE t.kind = 'integration'
    AND t.enabled
    AND t.config ->> 'integration' = sqlc.arg('integration')::text
    AND (
        t.connection_id IS NULL
        OR (c.id IS NOT NULL AND c.user_id = t.user_id AND c.owner_kind = 'user'
            AND c.deleted_at IS NULL AND c.status = 'active')
    )
ORDER BY t.id;

-- name: ListIntegrationTriggerOwners :many
-- The users with at least one enabled trigger of an integration: whose
-- access the periodic refresher keeps fresh.
SELECT DISTINCT t.user_id
FROM triggers t
WHERE t.kind = 'integration'
    AND t.enabled
    AND t.config ->> 'integration' = sqlc.arg('integration')::text
ORDER BY t.user_id;

-- name: ClaimIntegrationAccessRefresh :execrows
-- Takes the refresh lease for (user, integration) when no other replica
-- holds it and the last attempt is older than due_before. One row per pair;
-- the insert covers a pair never refreshed.
INSERT INTO integration_access_refresh (user_id, integration_id, leased_until, last_attempt_at)
VALUES (sqlc.arg('user_id')::text, sqlc.arg('integration_id')::text,
        sqlc.arg('leased_until')::timestamptz, sqlc.arg('now')::timestamptz)
ON CONFLICT (user_id, integration_id) DO UPDATE SET
    leased_until = EXCLUDED.leased_until,
    last_attempt_at = EXCLUDED.last_attempt_at
WHERE (integration_access_refresh.leased_until IS NULL OR integration_access_refresh.leased_until < sqlc.arg('now')::timestamptz)
    AND (integration_access_refresh.last_attempt_at IS NULL OR integration_access_refresh.last_attempt_at < sqlc.arg('due_before')::timestamptz);

-- name: FinishIntegrationAccessRefresh :exec
-- Releases the lease and records the outcome. refreshed_at moves only on
-- success; last_error is cleared on success.
UPDATE integration_access_refresh SET
    leased_until = NULL,
    refreshed_at = CASE WHEN sqlc.arg('ok')::boolean THEN sqlc.arg('at')::timestamptz ELSE refreshed_at END,
    last_error = sqlc.arg('last_error')::text
WHERE user_id = sqlc.arg('user_id')::text AND integration_id = sqlc.arg('integration_id')::text;

-- name: GetIntegrationAccessRefresh :one
SELECT * FROM integration_access_refresh
WHERE user_id = sqlc.arg('user_id')::text AND integration_id = sqlc.arg('integration_id')::text;

-- name: PruneIntegrationAccess :execrows
-- Drops grants nobody has re-confirmed since before: they no longer route
-- (routing requires a recent refresh) and their owner has no trigger left
-- to refresh them for.
DELETE FROM integration_event_access WHERE refreshed_at < sqlc.arg('before')::timestamptz;
