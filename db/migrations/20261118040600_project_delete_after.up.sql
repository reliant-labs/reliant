-- forge:backward-compatible — adds three nullable columns with no default and no constraint; the previous release never names them, so its reads, inserts and updates are unchanged, and after a rollback a project scheduled for deletion simply shows again (nothing is purged while no release reads delete_after).

-- Migration: project_delete_after
-- Created: 2026-11-18T04:06:00Z
--
-- Deleting a project has a 7-day restore window (control-plane
-- docs/design/unified-shell-and-projects.md §11.3 "Delete a project", D18).
-- DeleteProject no longer deletes: it sets delete_after, which hides the
-- project at once (every list, every read that authorizes it), pauses its
-- triggers (paused_reason 'project_deleted'), and leaves the rows for the
-- purge worker to remove once delete_after has passed. RestoreProject clears
-- it, until the purge begins.
--
--   delete_after      when the purge may run; NULL is a live project. Set
--                     once per deletion: a repeated delete keeps the first
--                     date.
--   deleted_by        who scheduled it: the owner, or an org admin deleting
--                     a departed member's project (D19). Lets the admin find
--                     it again under "Recently deleted".
--   purge_started_at  when a purge claimed the project (the worker, or
--                     PurgeProjectNow). From then on it cannot be restored:
--                     the purge deletes in batches, so a restore mid-purge
--                     would bring back a project missing some of its chats.
--                     A claim older than the worker's lease is taken again,
--                     so a purge a crashed worker left resumes.
--
-- No CHECK ties them together: the previous release writes none of them, and
-- readers treat purge_started_at as meaningful only while delete_after is set.
--
-- Adding a nullable column with no default touches only the catalog, but it
-- takes ACCESS EXCLUSIVE: lock_timeout bounds the wait behind a long
-- transaction, so the migration fails after 5s and the next deploy retries.
SET LOCAL lock_timeout = '5s';

ALTER TABLE projects
    ADD COLUMN delete_after timestamp with time zone,
    ADD COLUMN deleted_by text,
    ADD COLUMN purge_started_at timestamp with time zone;

COMMENT ON COLUMN projects.delete_after IS
    'When the project''s scheduled deletion may purge it; NULL for a live project. Set by DeleteProject, cleared by RestoreProject. A project with it set is hidden from every list and read.';
COMMENT ON COLUMN projects.deleted_by IS
    'Who scheduled the deletion (the owner, or an org admin for a departed member''s project); NULL while delete_after is NULL.';
COMMENT ON COLUMN projects.purge_started_at IS
    'When a purge claimed the scheduled project; it can no longer be restored. Meaningful only while delete_after is set.';
