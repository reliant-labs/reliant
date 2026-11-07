-- forge:backward-compatible — adds a table only this release reads and writes; the previous release never reads it, so after a rollback a detached machine is merely re-recorded by its next connect's reconcile, as it was before detaching existed.

-- Migration: project_daemon_detachments
--
-- A project_daemons row says "this machine holds the project's checkout, at
-- this path". DetachProjectFromDaemon drops that row WITHOUT touching the
-- folder on disk — a local checkout the user owns, which Reliant must forget
-- but never delete. Dropping the row alone does not stick: the folder is still
-- there, so the machine's next connect re-records it (the reconcile records
-- any machine with a directory at the project's creation path), the
-- plain-folder heal recreates it, and the machine's config watcher keeps
-- pushing snapshots for it.
--
-- A detachment is the record that the user said "not on this machine". The
-- connect reconcile, the heal, the checkout restore and the config snapshot
-- intake skip a detached (project, machine) pair, and a snapshot or discovery
-- for the detached path on that machine is ignored rather than read as a new
-- project. Any explicit attach, create or clone onto the machine records a
-- project_daemons row again and deletes the detachment in the same step, so a
-- pair never has both.
CREATE TABLE project_daemon_detachments (
    project_id text NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    daemon_id text NOT NULL REFERENCES daemons (id) ON DELETE CASCADE,
    path text NOT NULL,
    detached_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, daemon_id)
);

CREATE INDEX project_daemon_detachments_daemon ON project_daemon_detachments (daemon_id);

COMMENT ON TABLE project_daemon_detachments IS
    'A project the user detached from a machine: the folder stays on disk, and nothing re-records it there until the user attaches, creates or clones it on that machine again.';
COMMENT ON COLUMN project_daemon_detachments.path IS
    'The checkout path the machine had when it was detached: a snapshot or discovery for this path from this machine is ignored.';
