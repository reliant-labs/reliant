-- forge:backward-compatible — a new table plus nullable columns, a trigger and NOT VALID constraints; the previous release never names end_tenant_id, so its inserts leave it NULL, which every new constraint admits.
-- Migration: end_tenants_run_stamps
-- Created: 2026-11-26T05:34:10Z
--
-- The schema is the 240 migration(s) in this directory, applied in order (newest: 20261125211730_pipeline_repos_and_settings.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- GTM-R6 (docs/product/r6-tenant-scoped-design.md §2-§3, platform plan R6-2a),
-- the expand half. A host org runs workflows for its own customers ("end
-- tenants"). Tenant work is owned by the org's host service principal, a user
-- whose id carries the reserved prefix hsp_ (control-plane reserves it at every
-- door), and every row it owns is stamped with end_tenant_id. This file adds
-- the entity, the stamp columns and the constraints that make a wrong stamp
-- unrepresentable. No release reads or writes any of it yet: nothing here
-- changes behaviour.
--
-- Rows written before this release, and by the previous release during a
-- rolling deploy, have end_tenant_id NULL. Every constraint below admits NULL
-- (and the hsp_ CHECKs admit a user id without the prefix), so the previous
-- release keeps working on this schema and its rows are the ones it wrote.
--
-- Stamp invariants, per table of chats, workflows and triggers:
--   - the owner column carries hsp_ exactly when the row is stamped
--     (chats.user_id, triggers.user_id, workflows.owner_user_id; the
--     workflows CHECK says nothing for a NULL owner, so the chat's stamp,
--     copied by the trigger below, is what counts there);
--   - a stamped row has an org_id (a composite FK is not checked when any of
--     its columns is NULL, MATCH SIMPLE, so without this a stamped row with a
--     NULL org would skip the FK);
--   - the stamp must name a tenant of the row's own org: the composite FK
--     (end_tenant_id, org_id) -> end_tenants (id, host_org_id), so a stamp can
--     not point at another host's tenant;
--   - chats and triggers: a stamped row never uses a machine (no_machine),
--     until per-run isolation is proven (design OQ4). workflows has no
--     no_machine column; its run's machine is its chat's, which this CHECK
--     covers;
--   - chats: a service principal's chat has NULL visibility, so org viewers
--     (who see visibility='org' rows) never read a tenant's chat.
--
-- Every constraint is added NOT VALID so the ACCESS EXCLUSIVE/SHARE ROW
-- EXCLUSIVE locks cover the catalog change only; the next migration
-- (end_tenants_run_stamps_validate) validates them. lock_timeout bounds the
-- wait behind a long transaction: the migration fails after 5s and the next
-- deploy retries it. The indexes on chats, workflows and triggers follow in
-- files of their own (CONCURRENTLY).
--
-- connection_events, integration_writes and llm_call_usage are audit and
-- billing rows that outlive a tenant, so their end_tenant_id is a plain label
-- with no foreign key.
--
-- Rollback: none needed. The previous release never names these columns,
-- the trigger only fills a NULL, and dropping them is a later forward
-- migration, once no release reads them.
SET LOCAL lock_timeout = '5s';

-- A host org's end tenant. The (id, host_org_id) key exists for the composite
-- foreign keys below. external_ref is the host's own opaque reference to the
-- tenant (never authentication), unique within the host.
-- status 'deleting' starts the deletion grace: triggers stop at once, the
-- provider revoke and the crypto-shred run once delete_after passes, and the
-- host can restore (status back to 'active', delete_after NULL) before then.
CREATE TABLE end_tenants (
    id           TEXT PRIMARY KEY CHECK (id <> ''),
    host_org_id  TEXT NOT NULL CHECK (host_org_id <> ''),
    external_ref TEXT NOT NULL CHECK (external_ref <> ''),
    status       TEXT NOT NULL DEFAULT 'active',
    delete_after TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT end_tenants_status CHECK (status IN ('active', 'deleting')),
    CONSTRAINT end_tenants_deleting_has_delete_after
        CHECK ((status = 'deleting') = (delete_after IS NOT NULL)),
    CONSTRAINT end_tenants_id_host_org_id_key UNIQUE (id, host_org_id),
    CONSTRAINT end_tenants_host_org_id_external_ref_key UNIQUE (host_org_id, external_ref)
);
COMMENT ON TABLE end_tenants IS 'A host org''s end tenant: the customer a host (e.g. Growth) runs workflows for. Rows of chats, workflows and triggers are stamped with its id.';
COMMENT ON COLUMN end_tenants.host_org_id IS 'The organization (control-plane organizations.id) that owns the tenant; every stamped row''s org_id equals it.';
COMMENT ON COLUMN end_tenants.external_ref IS 'The host''s own opaque reference to the tenant. A label, never authentication.';
COMMENT ON COLUMN end_tenants.status IS 'active, or deleting: the deletion grace, in which triggers are stopped and the host can restore.';
COMMENT ON COLUMN end_tenants.delete_after IS 'When the deletion grace ends (revoke and crypto-shred run after it); set exactly while status is deleting.';

ALTER TABLE chats
    ADD COLUMN end_tenant_id text,
    ADD CONSTRAINT chats_end_tenant_fk FOREIGN KEY (end_tenant_id, org_id)
        REFERENCES end_tenants (id, host_org_id) NOT VALID,
    ADD CONSTRAINT chats_end_tenant_hsp_check
        CHECK (starts_with(user_id, 'hsp_') = (end_tenant_id IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT chats_end_tenant_has_org_check
        CHECK (end_tenant_id IS NULL OR org_id IS NOT NULL) NOT VALID,
    ADD CONSTRAINT chats_end_tenant_no_machine_check
        CHECK (end_tenant_id IS NULL OR no_machine) NOT VALID,
    ADD CONSTRAINT chats_hsp_visibility_check
        CHECK (visibility IS NULL OR NOT starts_with(user_id, 'hsp_')) NOT VALID;
COMMENT ON COLUMN chats.end_tenant_id IS 'The end tenant (end_tenants.id) the chat runs for; set exactly when user_id is a host service principal (hsp_). NULL: not tenant work.';

ALTER TABLE triggers
    ADD COLUMN end_tenant_id text,
    ADD CONSTRAINT triggers_end_tenant_fk FOREIGN KEY (end_tenant_id, org_id)
        REFERENCES end_tenants (id, host_org_id) NOT VALID,
    ADD CONSTRAINT triggers_end_tenant_hsp_check
        CHECK (starts_with(user_id, 'hsp_') = (end_tenant_id IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT triggers_end_tenant_has_org_check
        CHECK (end_tenant_id IS NULL OR org_id IS NOT NULL) NOT VALID,
    ADD CONSTRAINT triggers_end_tenant_no_machine_check
        CHECK (end_tenant_id IS NULL OR no_machine) NOT VALID;
COMMENT ON COLUMN triggers.end_tenant_id IS 'The end tenant (end_tenants.id) the trigger fires for; set exactly when user_id is a host service principal (hsp_). NULL: not tenant work.';

ALTER TABLE workflows
    ADD COLUMN end_tenant_id text,
    ADD CONSTRAINT workflows_end_tenant_fk FOREIGN KEY (end_tenant_id, org_id)
        REFERENCES end_tenants (id, host_org_id) NOT VALID,
    ADD CONSTRAINT workflows_end_tenant_hsp_check
        CHECK (owner_user_id IS NULL OR starts_with(owner_user_id, 'hsp_') = (end_tenant_id IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT workflows_end_tenant_has_org_check
        CHECK (end_tenant_id IS NULL OR org_id IS NOT NULL) NOT VALID;
COMMENT ON COLUMN workflows.end_tenant_id IS 'The end tenant the run belongs to: its chat''s, filled on insert (workflows_inherit_chat_end_tenant). NULL: not tenant work.';

-- A run inherits its chat's end tenant, as it inherits its chat's org
-- (workflows_inherit_chat_org). Triggers fire in name order, so this one runs
-- first; it does not depend on the org, and every CHECK and FK above runs
-- after both. A writer that names a stamp must name its chat's: an explicit
-- stamp that disagrees raises, whether the chat has another stamp or none.
CREATE OR REPLACE FUNCTION workflows_inherit_chat_end_tenant() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 chat_end_tenant text;
BEGIN
 SELECT c.end_tenant_id INTO chat_end_tenant FROM chats c WHERE c.id = NEW.chat_id;
 IF NEW.end_tenant_id IS NULL THEN
   NEW.end_tenant_id := chat_end_tenant;
 ELSIF NEW.end_tenant_id IS DISTINCT FROM chat_end_tenant THEN
   RAISE EXCEPTION 'workflows.end_tenant_id % disagrees with chat % end_tenant_id %',
     NEW.end_tenant_id, NEW.chat_id, COALESCE(chat_end_tenant, '<none>')
     USING ERRCODE = 'check_violation', CONSTRAINT = 'workflows_end_tenant_matches_chat';
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER workflows_inherit_chat_end_tenant BEFORE INSERT ON workflows
 FOR EACH ROW EXECUTE FUNCTION workflows_inherit_chat_end_tenant();

-- Audit, write-ledger and usage rows: a label naming the tenant the row was
-- written for, kept after the tenant is gone. No foreign key.
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
