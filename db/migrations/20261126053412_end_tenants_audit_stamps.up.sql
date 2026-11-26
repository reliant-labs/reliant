-- forge:backward-compatible — nullable columns and NOT VALID constraints the previous release never names; its inserts leave them NULL, which every new constraint admits.
-- Migration: end_tenants_audit_stamps
-- Created: 2026-11-26T05:34:10Z
--
-- The schema is the 240 migration(s) in this directory, applied in order (newest: 20261125211730_pipeline_repos_and_settings.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- GTM-R6 (R6-2a), the audit half: connection_events, integration_writes
-- (which also gains org_id) and llm_call_usage record the end tenant a row was
-- written for. Each is a plain nullable label with a non-blank CHECK and no
-- foreign key: these are audit and billing rows that outlive a tenant (R6-8
-- deletes the tenant, not its history). That makes the stamp an unchecked
-- label, so the only writer must be the code that reads it off the run row
-- (R6-11).
--
-- Kept apart from end_tenants_run_stamps so its ACCESS EXCLUSIVE locks on
-- chats, triggers and workflows are not held while these three tables are
-- waited for. The CHECKs are added NOT VALID; end_tenants_audit_stamps_validate
-- validates them. lock_timeout bounds each wait: the migration fails after 5s
-- and the next deploy retries it.
--
-- Rollback: none needed. The previous release never names these columns.
SET LOCAL lock_timeout = '5s';

ALTER TABLE connection_events
    ADD COLUMN end_tenant_id text,
    ADD CONSTRAINT connection_events_end_tenant_id_not_blank CHECK (end_tenant_id <> '') NOT VALID;
COMMENT ON COLUMN connection_events.end_tenant_id IS 'The end tenant the connection use was for. NULL: not tenant work.';

ALTER TABLE integration_writes
    ADD COLUMN org_id text,
    ADD COLUMN end_tenant_id text,
    ADD CONSTRAINT integration_writes_org_id_not_blank CHECK (org_id <> '') NOT VALID,
    ADD CONSTRAINT integration_writes_end_tenant_id_not_blank CHECK (end_tenant_id <> '') NOT VALID;
COMMENT ON COLUMN integration_writes.org_id IS 'The organization the write was made in. NULL: written before this column.';
COMMENT ON COLUMN integration_writes.end_tenant_id IS 'The end tenant the write was for. NULL: not tenant work.';

ALTER TABLE llm_call_usage
    ADD COLUMN end_tenant_id text,
    ADD CONSTRAINT llm_call_usage_end_tenant_id_not_blank CHECK (end_tenant_id <> '') NOT VALID;
COMMENT ON COLUMN llm_call_usage.end_tenant_id IS 'The end tenant the model call was made for, to roll usage up per tenant. NULL: not tenant work.';
