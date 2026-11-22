-- forge:backward-compatible — validates the widened scope constraint without changing accepted rows.

-- Migration: access_tokens_mcp_oauth_scope_validate
-- Created: 2026-11-22T17:33:05Z
--
-- The schema is the 209 migration(s) in this directory, applied in order (newest: 20261122173252_access_tokens_mcp_oauth_scope.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Scan existing rows separately from the catalog change so readers and writers
-- are not blocked by the validation scan.
SET LOCAL lock_timeout = '5s';
ALTER TABLE access_tokens VALIDATE CONSTRAINT access_tokens_scopes;

