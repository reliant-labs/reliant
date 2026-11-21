-- forge:backward-compatible — widens access_tokens_scopes to exactly accesstoken.AllScopes (adds 'changes:write', 'changes:launch', 'vuln:read', 'tests:read', 'tests:write'); every row the previous release wrote still satisfies it.
-- Migration: access_tokens_v190_scopes
-- Created: 2026-11-21T08:20:48Z
--
-- The schema is the 204 migration(s) in this directory, applied in order (newest: 20261121122326_pr_records.up.sql). To read it:
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
--
-- The new scopes arrive with the forge bump that carries this file (forge
-- v1.9: accesstoken.AllScopes gained changes:write, changes:launch, vuln:read,
-- tests:read and tests:write). reliant does not mint them today; the CHECK
-- follows the set so `forge lint --closed-sets` holds and a later release may.
-- lock_timeout bounds the wait for the lock, as in
-- 20261111183002_access_tokens_sourcemaps_scope: behind a long transaction
-- the migration fails after 5s (and the next deploy retries it) instead of
-- queueing every token read behind it.
SET LOCAL lock_timeout = '5s';
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
            'tests:write'
        ]::text[]
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
