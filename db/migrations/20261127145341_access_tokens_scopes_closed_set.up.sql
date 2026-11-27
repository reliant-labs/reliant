-- forge:backward-compatible — widens access_tokens_scopes to exactly accesstoken.AllScopes (adds 'sbom:write'); every row the previous release wrote still satisfies it.
-- Migration: access_tokens_scopes_closed_set
-- Created: 2026-11-27T14:53:41Z
--
-- The schema is the 255 migration(s) in this directory, applied in order (newest: 20261126225458_record_due_trigger_kind_validate.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- access_tokens_scopes on access_tokens mirrors forge:closed-set accesstoken.AllScopes.
-- Replace it with a CHECK that lists exactly that set, in the set's order.
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
            'changes:read',
            'sbom:write'
        ]::text[]
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
