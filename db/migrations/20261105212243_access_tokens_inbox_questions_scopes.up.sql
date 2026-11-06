-- forge:backward-compatible — widens access_tokens_scopes to exactly accesstoken.AllScopes (adds 'inbox:read', 'inbox:manage', 'approvals:decide', 'questions:answer'); every row the previous release wrote still satisfies it.
-- Migration: access_tokens_inbox_questions_scopes
-- Created: 2026-11-05T21:22:43Z
--
-- The schema is the 93 migration(s) in this directory, applied in order (newest: 20261105063955_access_tokens_runs_triggers_scopes_validate.up.sql). To read it:
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
--
-- The new CHECK is added NOT VALID, so this ACCESS EXCLUSIVE lock is held only
-- for the catalog change, never for a scan of the table, and lock_timeout
-- bounds the wait for it: behind a long transaction the migration fails
-- after 5s (and is retried by the next deploy) instead of queueing every
-- token read behind it. New and updated rows are checked from this commit on.
-- 20261105212256_access_tokens_inbox_questions_scopes_validate validates the
-- existing rows in its own transaction (a migration file is one), under
-- SHARE UPDATE EXCLUSIVE, which does not block reads or writes. Every
-- existing row already satisfied the narrower CHECK this replaces, so
-- validation cannot fail.
SET LOCAL lock_timeout = '5s';
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'deploy:write',
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
            'runs:start',
            'runs:read',
            'runs:control',
            'triggers:fire',
            'triggers:manage',
            'inbox:read',
            'inbox:manage',
            'approvals:decide',
            'questions:answer'
        ]::text[]
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
