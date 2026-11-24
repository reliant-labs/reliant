-- forge:backward-compatible — adds a new table that nothing in the previous release reads or writes.

-- The nested repositories of a project being set up on a machine: one row per
-- repository below the project root that has a remote, while the clone of the
-- root on that machine (project_daemons) is the unit the rest of the product
-- reads. CreateProjectFromRepo writes them pending beside the queued root
-- clone; once the root has landed, the machine's gateway clones each one into
-- its relative path and records how it went here, and only then settles the
-- project_daemons row (installed, or failed naming the repository). A detach
-- of the machine, or the repository's deletion, takes the rows with it.
CREATE TABLE project_daemon_repos (
    project_id TEXT NOT NULL,
    daemon_id TEXT NOT NULL,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    -- pending: waiting for the root clone; cloning: a gateway claimed it;
    -- installed: on the machine's disk; failed: see error.
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'cloning', 'installed', 'failed')),
    -- git's own words for a failed clone; empty otherwise.
    error TEXT NOT NULL DEFAULT '',
    -- When state last changed: a claim older than the clone timeout is the
    -- claim of a gateway that went away, and may be taken again.
    state_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, daemon_id, repo_id),
    FOREIGN KEY (project_id, daemon_id) REFERENCES project_daemons (project_id, daemon_id) ON DELETE CASCADE
);

COMMENT ON TABLE project_daemon_repos IS 'forge:no-entity';
CREATE INDEX project_daemon_repos_daemon_idx ON project_daemon_repos (daemon_id) WHERE state IN ('pending', 'cloning');
