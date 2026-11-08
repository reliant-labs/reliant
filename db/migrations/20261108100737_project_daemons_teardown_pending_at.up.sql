-- forge:backward-compatible — one nullable column the previous release neither reads nor writes; its inserts and updates of project_daemons name their columns and still succeed.

-- Migration: project_daemons_teardown_pending_at
--
-- A remove or reclone of a project's checkout on a machine deletes the
-- checkout on that disk, and the project's branch workspaces there are git
-- worktrees of it. The server holds the checkout's exclusive lock while it
-- waits for the machine, so no workspace is admitted meanwhile. But the wait
-- is bounded (the daemon command's request budget), and the machine keeps
-- deleting after it: a workspace admitted once the lock was released could
-- still lose its common repository.
--
-- teardown_pending_at is set, committed, before the destructive command is
-- sent, and cleared only once the machine has answered (or the command
-- provably never reached it). While it is set a branch workspace of the
-- checkout is refused. A timeout, a cancelled wait or a server crash leaves
-- it set: running the remove or reclone again settles it.
ALTER TABLE project_daemons ADD COLUMN teardown_pending_at timestamp with time zone;

COMMENT ON COLUMN project_daemons.teardown_pending_at IS
    'When a remove or reclone of the checkout was sent whose outcome has not been heard. NULL when none is pending. A branch workspace of the checkout is refused while it is set.';
