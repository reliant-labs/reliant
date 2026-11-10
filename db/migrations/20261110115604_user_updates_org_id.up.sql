-- forge:backward-compatible — adds a nullable column, a NOT VALID check every existing row (all NULL) satisfies, and a BEFORE INSERT trigger that only fills user_updates.org_id; the previous release never names org_id and its inserts keep succeeding.

-- Migration: user_updates_org_id
-- Created: 2026-11-10T11:56:04Z
--
-- A user update carries the org of what it is about, stamped when it is
-- written, so the user stream authorizes a replayed update from the update
-- itself (internal/handlers/streaming orgGate): an update about a chat,
-- project or saved workflow that has since been deleted still says which org
-- it belonged to, and a member who has left that org does not get its
-- history.
--
--   - user_updates.org_id: the org of the chat the update names, else of its
--     project, else of the saved workflow it is about (entity_type 6,
--     ENTITY_TYPE_WORKFLOW_DRAFT); NULL when none has one. The trigger below
--     fills it on insert when the writer named none, which is every writer,
--     including the previous release during a rolling deploy.
--
-- Expand only: rows written before this release keep NULL (no backfill); the
-- stream falls back to the resource's current org for those, and withholds
-- them when that cannot be read.
--
-- The CHECK keeps '' out, added NOT VALID so the ACCESS EXCLUSIVE lock covers
-- only the catalog change; it is validated in the next migration.
-- lock_timeout bounds the wait behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE user_updates
    ADD COLUMN org_id text,
    ADD CONSTRAINT user_updates_org_id_not_blank CHECK (org_id <> '') NOT VALID;
COMMENT ON COLUMN user_updates.org_id IS
    'The organization the update is about, stamped on insert (user_updates_stamp_org): its chat''s, else its project''s, else its saved workflow''s. NULL: written before orgs, or about nothing of an org.';

-- Three primary-key reads at most per inserted update, only when the writer
-- named no org.
CREATE OR REPLACE FUNCTION user_updates_stamp_org() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.org_id IS NULL THEN
   NEW.org_id := COALESCE(
     (SELECT c.org_id FROM chats c WHERE c.id = NEW.chat_id),
     (SELECT p.org_id FROM projects p WHERE p.id = NEW.project_id),
     CASE WHEN NEW.entity_type = 6 THEN (SELECT d.org_id FROM workflow_drafts d WHERE d.id = NEW.entity_id) END);
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER user_updates_stamp_org BEFORE INSERT ON user_updates
 FOR EACH ROW EXECUTE FUNCTION user_updates_stamp_org();
