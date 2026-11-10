-- forge:backward-compatible — adds nullable columns, NOT VALID checks every existing row (all NULL) satisfies, and a BEFORE INSERT trigger that only fills workflows.org_id from its chat; the previous release never names org_id and its inserts keep succeeding.

-- Migration: org_id_workflows_triggers_runs
-- Created: 2026-11-10T03:34:39Z
--
-- Saved workflows, triggers and workflow runs belong to an organization too
-- (owner decision, 2026-11-10), as projects and chats do
-- (20261110021244_org_id_projects_chats). The org is control-plane's: an id,
-- no foreign key, no copy of membership.
--
--   - workflow_drafts.org_id: a saved workflow's org, fixed at creation.
--   - triggers.org_id: a trigger's org, fixed at creation (its project's
--     when the project has one). Every chat it launches belongs to it, so a
--     scheduled run's spend bills the trigger's org.
--   - workflows.org_id: a run's org. Every run has a chat, and a run
--     inherits its chat's org: the trigger below fills it on insert when the
--     writer named none, which is every writer, including the previous
--     release during a rolling deploy. A chat created with no org leaves its
--     runs with none until the chat is stamped (internal/orgs, lazy
--     backfill, which stamps the runs with it).
--
-- NULL is every row written before this release. It means what it meant
-- before: the owner's alone, in their default org.
--
-- The CHECKs keep '' out, added NOT VALID so the ACCESS EXCLUSIVE lock covers
-- only the catalog change; 20261110033451_org_id_workflows_triggers_runs_validate
-- validates them. lock_timeout bounds the wait behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE workflow_drafts
    ADD COLUMN org_id text,
    ADD CONSTRAINT workflow_drafts_org_id_not_blank CHECK (org_id <> '') NOT VALID;
COMMENT ON COLUMN workflow_drafts.org_id IS
    'The organization (control-plane organizations.id) the saved workflow belongs to, fixed at creation. NULL: written before orgs, or no org could be resolved.';

ALTER TABLE triggers
    ADD COLUMN org_id text,
    ADD CONSTRAINT triggers_org_id_not_blank CHECK (org_id <> '') NOT VALID;
COMMENT ON COLUMN triggers.org_id IS
    'The organization the trigger belongs to, fixed at creation; every chat it launches belongs to it and bills it. NULL: written before orgs, or no org could be resolved.';

ALTER TABLE workflows
    ADD COLUMN org_id text,
    ADD CONSTRAINT workflows_org_id_not_blank CHECK (org_id <> '') NOT VALID;
COMMENT ON COLUMN workflows.org_id IS
    'The organization the run belongs to: its chat''s, filled on insert (workflows_inherit_chat_org). NULL: written before orgs, or its chat has no org yet.';

-- A run inherits its chat's org. One primary-key read of the chat per
-- inserted run, only when the writer named no org.
CREATE OR REPLACE FUNCTION workflows_inherit_chat_org() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.org_id IS NULL THEN
   SELECT c.org_id INTO NEW.org_id FROM chats c WHERE c.id = NEW.chat_id;
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER workflows_inherit_chat_org BEFORE INSERT ON workflows
 FOR EACH ROW EXECUTE FUNCTION workflows_inherit_chat_org();
