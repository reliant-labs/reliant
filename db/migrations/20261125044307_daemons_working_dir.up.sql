-- forge:backward-compatible — adds a nullable column that the previous release neither reads nor writes.

-- The working directory the daemon reported when it last registered
-- (DaemonRegister.working_dir: DAEMON_WORKING_DIR, else the process's cwd).
-- CreateProjectFromRepo clones a project that names no path into it on a
-- machine that is not a managed one, instead of the managed machines'
-- /home/workspace/projects. NULL until the machine registers with a release
-- that records it.
ALTER TABLE daemons ADD COLUMN working_dir TEXT;
