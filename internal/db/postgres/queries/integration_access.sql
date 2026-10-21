-- Access-gated routing for app-level provider events. See the migration
-- 20261005061008_integration_event_access.sql for the model.

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
