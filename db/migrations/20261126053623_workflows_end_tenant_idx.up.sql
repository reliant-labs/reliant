-- forge:backward-compatible — a new partial index over rows the previous release never writes; it refuses no write and no statement of the previous release names it.
-- Migration: workflows_end_tenant_idx
-- Created: 2026-11-26T05:36:22Z
--
-- The schema is the 243 migration(s) in this directory, applied in order (newest: 20261126053622_chats_end_tenant_idx.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- The rows of one end tenant: the foreign key to end_tenants, and the per-tenant
-- reads and the deletion cascade that follow (R6-8). Partial, so it holds only
-- stamped rows and costs nothing while no tenant exists.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that IF
-- NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY
-- workflows_end_tenant_idx and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS workflows_end_tenant_idx
    ON public.workflows (end_tenant_id)
    WHERE end_tenant_id IS NOT NULL;
