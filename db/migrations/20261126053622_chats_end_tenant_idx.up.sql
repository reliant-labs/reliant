-- forge:backward-compatible — a new partial index over rows the previous release never writes; it refuses no write and no statement of the previous release names it.
-- Migration: chats_end_tenant_idx
-- Created: 2026-11-26T05:36:22Z
--
-- The schema is the 242 migration(s) in this directory, applied in order (newest: 20261126053411_end_tenants_run_stamps_validate.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- One end tenant's chats: the referencing-side index of the composite foreign key
-- and the per-tenant reads. Partial, so it costs nothing while no tenant
-- exists. A lone CONCURRENTLY statement runs outside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS chats_end_tenant_idx
    ON public.chats (end_tenant_id)
    WHERE end_tenant_id IS NOT NULL;
