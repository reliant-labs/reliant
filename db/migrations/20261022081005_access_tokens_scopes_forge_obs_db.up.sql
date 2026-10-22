-- forge:backward-compatible — widens access_tokens_scopes to exactly accesstoken.AllScopes (adds 'obs:read', 'obs:triage', 'db:query'); every row the previous release wrote still satisfies it.
-- Migration: access_tokens_scopes_forge_obs_db
-- Created: 2026-10-22T07:36:33Z
--
-- The schema is the 28 migration(s) in this directory, applied in order (newest: 20261022040519_question_withdrawn_reason.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Admit obs:read, obs:triage and db:query in access_tokens.scopes: forge #1121
-- (a46dda1d) added them to forge/pkg/accesstoken.AllScopes, which this forge
-- pin brings in, so a mint of any of them here would fail the CHECK.
-- Written by `forge db migration new --closed-set access_tokens_scopes`.

-- access_tokens_scopes on access_tokens mirrors forge:closed-set accesstoken.AllScopes.
-- Replace it with a CHECK that lists exactly that set, in the set's order.
-- DROP and ADD are one ALTER TABLE: one lock, and no instant
-- without the constraint. postgres drops a constraint's comment with it, so the
-- declaration is re-issued after.
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'deploy:write',
            'cluster:manage',
            'token:read',
            'token:write',
            'reliant:api',
            'daemon:connect',
            'llm:invoke',
            'proxy:port',
            'mcp:connector',
            'daemon:resume',
            'secret:read',
            'secret:write',
            'domain:read',
            'domain:write',
            'identity:read',
            'identity:manage',
            'obs:read',
            'obs:triage',
            'db:query'
        ]::text[]
    );
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
