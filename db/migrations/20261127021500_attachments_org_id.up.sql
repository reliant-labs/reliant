-- forge:backward-compatible — adds a nullable column; nothing existing is changed

-- Migration: attachments_org_id
-- Created: 2026-11-27T02:15:00Z
--
-- An attachment records the organization it was made in, so a read that acts
-- in one org (GTM's attach-from-chat, store.GetAttachmentForUserInOrg) can
-- take only that org's attachments. It is stamped at creation: by the upload
-- RPCs from the request's org_id once the caller is verified a live member of
-- it, and by the agent's media tools from their chat's org.
--
-- An id, no foreign key, as on every other org-scoped table
-- (20261110033439_org_id_workflows_triggers_runs).
--
-- NULL is every row written before this release, and every row made with no
-- org (an upload naming none, a chat in no org). Nothing is backfilled: no
-- link from an attachment to the chat it was sent in is recorded anywhere to
-- backfill from. An org-scoped read never matches a NULL row.
--
-- The CHECK is added NOT VALID, so the ACCESS EXCLUSIVE lock covers only the
-- catalog change; 20261127021505_attachments_org_id_validate validates it.
-- 20261127021510_attachments_user_org_index adds the (user_id, org_id)
-- index CONCURRENTLY, in a file of its own. lock_timeout bounds the wait
-- behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE attachments
    ADD COLUMN org_id text,
    ADD CONSTRAINT attachments_org_id_not_blank CHECK (org_id IS NULL OR org_id <> '') NOT VALID;
COMMENT ON COLUMN attachments.org_id IS
    'The organization (control-plane organizations.id) the attachment was made in. NULL: made in no org, or written before this column.';
