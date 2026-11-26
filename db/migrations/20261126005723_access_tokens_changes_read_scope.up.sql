-- forge:backward-compatible — widens access_tokens_scopes to exactly accesstoken.AllScopes (adds 'changes:read'); every row the previous release wrote still satisfies it.
-- Migration: access_tokens_changes_read_scope
-- Created: 2026-11-26T00:57:23Z
--
-- The schema is the 240 migration(s) in this directory, applied in order (newest: 20261125211730_pipeline_repos_and_settings.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- access_tokens_scopes on access_tokens mirrors forge:closed-set accesstoken.AllScopes.
-- Replace it with a CHECK that lists exactly that set, in the set's order.
-- Predecessor at BASE: 20261123015915_access_tokens_mcp_scopes.up.sql
-- (validated by 20261123015916_access_tokens_mcp_scopes_validate.up.sql), the
-- last migration to touch access_tokens_scopes. That CHECK requires
-- cardinality(scopes) > 0 AND scopes <@ ARRAY[42 scopes]::text[].
-- This keeps the same predicate and all 42 values in the same order, adding only
-- changes:read (forge #2834), for 43 values in all. No row that was accepted before
-- is excluded, so the separate validation is safe for existing rows and for the
-- previous release's writes. No row or column is changed.
-- DROP and ADD are one ALTER TABLE: one lock, and no instant
-- without the constraint. postgres drops a constraint's comment with it, so the
-- declaration is re-issued after.
-- The CHECK is added NOT VALID, so this ACCESS EXCLUSIVE lock is held for the
-- catalog change alone, not for a scan of the table; new and updated rows are
-- checked from this commit on. The next migration validates the existing rows.
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'deploy:write',
            'deploy:config:read',
            'billing:read',
            'cluster:manage',
            'token:read',
            'token:write',
            'reliant:api',
            'daemon:connect',
            'llm:invoke',
            'proxy:port',
            'mcp:connector',
            'mcp:oauth',
            'daemon:resume',
            'daemon:manage',
            'secret:read',
            'secret:write',
            'domain:read',
            'domain:write',
            'identity:read',
            'identity:manage',
            'obs:read',
            'obs:triage',
            'obs:write',
            'db:query',
            'storage:read',
            'runs:start',
            'runs:read',
            'runs:control',
            'triggers:fire',
            'triggers:manage',
            'inbox:read',
            'inbox:manage',
            'approvals:decide',
            'questions:answer',
            'sourcemaps:write',
            'changes:write',
            'changes:launch',
            'vuln:read',
            'tests:read',
            'tests:write',
            'mcp:forge',
            'changes:read'
        ]::text[]
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
