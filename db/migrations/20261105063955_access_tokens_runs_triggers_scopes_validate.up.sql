-- forge:backward-compatible — validates existing rows against the widened access_tokens_scopes; every row already satisfied the narrower CHECK it replaced, and nothing is written.
-- Migration: access_tokens_runs_triggers_scopes_validate
-- Created: 2026-11-05T06:39:55Z
--
-- The schema is the 92 migration(s) in this directory, applied in order (newest: 20261105051858_access_tokens_runs_triggers_scopes.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- 20261105051858_access_tokens_runs_triggers_scopes added the widened
-- access_tokens_scopes NOT VALID. Validate it here, in its own transaction:
-- VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which does not block token
-- reads or writes while it scans. lock_timeout bounds the wait.
SET LOCAL lock_timeout = '5s';
ALTER TABLE access_tokens VALIDATE CONSTRAINT access_tokens_scopes;
