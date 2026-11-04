-- forge:backward-compatible — one nullable column the previous release neither reads nor writes; its inserts and updates of project_daemons name their columns and still succeed.

-- Migration: project_daemons_clone_started_at
--
-- The connect reconcile settles a clone whose outcome never arrived once it
-- has been installing for 30 minutes. It counted from cloned_at, the time the
-- clone was QUEUED. A clone queued for a sleeping machine only starts when the
-- machine wakes, so a machine asleep for more than 30 minutes woke to the
-- reconcile marking its just-starting clone failed ("did not report back")
-- until the clone's own outcome corrected it.
--
-- clone_started_at is when the clone could first run: the queue time when its
-- machine was connected then, otherwise the machine's next connect (stamped by
-- that connect's reconcile). NULL while the clone waits for a machine that has
-- not connected since. The stuck-clone timeout counts from here.
--
-- No backfill: a clone already installing when this lands reads NULL, the
-- same as one queued while its machine was away, and its machine's next
-- connect starts its clock (StartQueuedProjectDaemonClones). So the migration
-- is a catalog-only ADD COLUMN and holds its lock for no table scan. A clone
-- whose outcome was lost then settles 30 minutes after that connect instead
-- of at it, which the timeout already allows for any clone.
ALTER TABLE project_daemons ADD COLUMN clone_started_at timestamp with time zone;

COMMENT ON COLUMN project_daemons.clone_started_at IS
    'When an installing clone could first run: queued to a connected machine, or the machine''s next connect. NULL while it waits for a machine. The stuck-clone timeout counts from here.';
