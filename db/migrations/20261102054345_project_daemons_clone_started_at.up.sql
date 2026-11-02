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
ALTER TABLE project_daemons ADD COLUMN clone_started_at timestamp with time zone;

COMMENT ON COLUMN project_daemons.clone_started_at IS
    'When an installing clone could first run: queued to a connected machine, or the machine''s next connect. NULL while it waits for a machine. The stuck-clone timeout counts from here.';

-- Clones already in flight keep the clock they had.
UPDATE project_daemons SET clone_started_at = cloned_at WHERE install_state = 'installing';
