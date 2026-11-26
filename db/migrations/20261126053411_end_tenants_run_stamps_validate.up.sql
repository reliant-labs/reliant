-- forge:backward-compatible — validates the constraints the previous migration added NOT VALID; nothing is written, and every existing row has end_tenant_id NULL.
-- Migration: end_tenants_run_stamps_validate
-- Created: 2026-11-26T05:34:10Z
--
-- The schema is the 241 migration(s) in this directory, applied in order (newest: 20261126053410_end_tenants_run_stamps.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- 20261126053410_end_tenants_run_stamps added these constraints NOT VALID.
-- Validate them here, in their own transaction: VALIDATE CONSTRAINT holds SHARE
-- UPDATE EXCLUSIVE (SHARE UPDATE EXCLUSIVE on the referenced end_tenants and
-- ROW SHARE for the foreign keys), which blocks neither reads nor writes while
-- it scans. Every existing row has end_tenant_id NULL, so each constraint is
-- satisfied by construction, with one exception that is checked rather than
-- assumed: the hsp_ CHECKs fail if a user id already carries the reserved
-- prefix. None can, since control-plane refuses the prefix at login; a
-- failure here means something minted one.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed): no constraint on the stamped tables is left unvalidated (0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify end tenant constraints validated" \
--     "SELECT count(*) FROM pg_constraint WHERE NOT convalidated AND conrelid IN ('chats'::regclass, 'workflows'::regclass, 'triggers'::regclass, 'connection_events'::regclass, 'integration_writes'::regclass, 'llm_call_usage'::regclass) AND (conname LIKE '%end_tenant%' OR conname IN ('chats_hsp_visibility_check', 'integration_writes_org_id_not_blank'))"
--
-- Rollback: none needed; nothing is written.
ALTER TABLE chats VALIDATE CONSTRAINT chats_end_tenant_fk;
ALTER TABLE chats VALIDATE CONSTRAINT chats_end_tenant_hsp_check;
ALTER TABLE chats VALIDATE CONSTRAINT chats_end_tenant_has_org_check;
ALTER TABLE chats VALIDATE CONSTRAINT chats_end_tenant_no_machine_check;
ALTER TABLE chats VALIDATE CONSTRAINT chats_hsp_visibility_check;
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_end_tenant_fk;
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_end_tenant_hsp_check;
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_end_tenant_has_org_check;
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_end_tenant_no_machine_check;
ALTER TABLE workflows VALIDATE CONSTRAINT workflows_end_tenant_fk;
ALTER TABLE workflows VALIDATE CONSTRAINT workflows_end_tenant_hsp_check;
ALTER TABLE workflows VALIDATE CONSTRAINT workflows_end_tenant_has_org_check;
ALTER TABLE connection_events VALIDATE CONSTRAINT connection_events_end_tenant_id_not_blank;
ALTER TABLE integration_writes VALIDATE CONSTRAINT integration_writes_org_id_not_blank;
ALTER TABLE integration_writes VALIDATE CONSTRAINT integration_writes_end_tenant_id_not_blank;
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_end_tenant_id_not_blank;
