-- forge:backward-compatible — validates the constraints the previous migration added NOT VALID; nothing is written, and every existing row has end_tenant_id NULL.
-- Migration: end_tenants_audit_stamps_validate
-- Created: 2026-11-26T05:34:10Z
--
-- The schema is the 241 migration(s) in this directory, applied in order (newest: 20261126053410_end_tenants_run_stamps.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- 20261126053412_end_tenants_audit_stamps added these constraints NOT VALID.
-- Validate them here, in their own transaction: VALIDATE CONSTRAINT holds SHARE
-- UPDATE EXCLUSIVE, which blocks neither reads nor writes while it scans.
-- Every existing row has the new columns NULL, so each is satisfied by
-- construction.
--
-- Rollback: none needed; nothing is written.
ALTER TABLE connection_events VALIDATE CONSTRAINT connection_events_end_tenant_id_not_blank;
ALTER TABLE integration_writes VALIDATE CONSTRAINT integration_writes_org_id_not_blank;
ALTER TABLE integration_writes VALIDATE CONSTRAINT integration_writes_end_tenant_id_not_blank;
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_end_tenant_id_not_blank;
