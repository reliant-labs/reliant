-- forge:backward-compatible — validates existing rows against the widened access_tokens_scopes; every row already satisfied the narrower CHECK it replaced, and nothing is written.
-- Migration: access_tokens_mcp_scopes_validate
-- Created: 2026-11-23T01:59:15Z
--
-- The schema is the 209 migration(s) in this directory, applied in order (newest: 20261123015915_access_tokens_mcp_scopes.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- The previous migration added the widened access_tokens_scopes NOT VALID. Validate it here,
-- in its own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which
-- blocks neither reads nor writes of access_tokens while it scans.
ALTER TABLE access_tokens VALIDATE CONSTRAINT access_tokens_scopes;
