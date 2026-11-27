-- forge:backward-compatible — adds a nullable column; nothing existing is changed

-- Migration: attachments_org_id
-- Created: 2026-11-27T23:04:33Z
--
-- An attachment records an organization at creation, so a read that acts in
-- one org (GTM's attach-from-chat, store.GetAttachmentForUserInOrg) can take
-- only attachments stamped with that org. The stamp says the owner was a live
-- member of the org when the attachment was stored, and nothing more: not
-- which chat it came from or was sent in, and not that the owner is a member
-- still. The upload RPCs stamp the org the request names once the caller is
-- verified a live member of it (an unverifiable request stamps none); the
-- agent's media tools stamp the org of the chat they ran in.
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
-- catalog change; 20261127230434_attachments_org_id_validate validates it.
-- 20261127230435_attachments_user_org_index adds the (user_id, org_id)
-- index CONCURRENTLY, in a file of its own. lock_timeout bounds the wait
-- behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE attachments
    ADD COLUMN org_id text,
    ADD CONSTRAINT attachments_org_id_not_blank CHECK (org_id IS NULL OR org_id <> '') NOT VALID;
COMMENT ON COLUMN attachments.org_id IS
    'An organization (control-plane organizations.id) the owner was a live member of when the attachment was stored. Not which chat it came from. NULL: none named or verified, or written before this column.';
