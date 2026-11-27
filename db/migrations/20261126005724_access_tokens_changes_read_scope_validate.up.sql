-- forge:backward-compatible — validates existing rows against the widened access_tokens_scopes; every row already satisfied the narrower CHECK it replaced, and nothing is written.
-- Migration: access_tokens_changes_read_scope_validate
-- Created: 2026-11-26T00:57:24Z
--
-- The schema is the 241 migration(s) in this directory, applied in order (newest: 20261126005723_access_tokens_changes_read_scope.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- The previous migration added the widened access_tokens_scopes NOT VALID. Validate it here,
-- in its own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which
-- blocks neither reads nor writes of access_tokens while it scans.
ALTER TABLE access_tokens VALIDATE CONSTRAINT access_tokens_scopes;
