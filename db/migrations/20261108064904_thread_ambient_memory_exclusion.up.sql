-- forge:backward-compatible — additive column with false default; old binaries keep their existing behavior.

-- Migration: thread_ambient_memory_exclusion
-- Created: 2026-11-07T12:26:06Z
--
-- The schema is the 96 migration(s) in this directory, applied in order (newest: 20261105212256_access_tokens_inbox_questions_scopes_validate.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Monotonic thread policy survives new runs, coarse recovery and stopped-agent
-- resumption. There is deliberately no reset operation.
ALTER TABLE threads ADD COLUMN exclude_ambient_memory boolean NOT NULL DEFAULT false;
