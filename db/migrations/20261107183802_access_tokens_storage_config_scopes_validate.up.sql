-- forge:backward-compatible — validates the widened scope CHECK; every old row already satisfied the narrower constraint
-- Migration: access_tokens_storage_config_scopes_validate
-- Created: 2026-11-07T18:38:02Z
--
-- The schema is the 97 migration(s) in this directory, applied in order (newest: 20261107183754_access_tokens_storage_config_scopes.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Validate in a separate transaction under SHARE UPDATE EXCLUSIVE, without
-- blocking normal token reads/writes during the scan.
SET LOCAL lock_timeout = '5s';
ALTER TABLE access_tokens VALIDATE CONSTRAINT access_tokens_scopes;

