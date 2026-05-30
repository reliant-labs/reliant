-- +goose Up
--
-- Make projects portable across cloud daemons by identifying them by their git
-- remote URL. Adds `remote_url` to projects and introduces a join table
-- `project_daemons` tracking which daemons have a clone of the project.
--
-- TODO(portable-projects): Backfill remote_url for existing projects on the
-- next daemon sync — read .git/config from each daemon's clone of a project
-- and write the resolved URL into projects.remote_url + insert a
-- project_daemons row for that (project, daemon, path) pair. The backfill is
-- intentionally not run from this migration because the remote URL lives on
-- the daemon's filesystem, not in the DB.

ALTER TABLE projects ADD COLUMN remote_url TEXT;

-- Partial unique index: enforce one project per (user, remote_url) so a user
-- can't accidentally onboard the same git remote twice, while still allowing
-- many projects with NULL remote_url (non-git projects, or projects whose
-- remote hasn't been resolved yet).
CREATE UNIQUE INDEX projects_user_remote_url_uniq
    ON projects(user_id, remote_url)
    WHERE remote_url IS NOT NULL;

CREATE TABLE project_daemons (
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    daemon_id      TEXT NOT NULL,                     -- matches project_configs.daemon_id (no FK; daemons table may be ephemeral)
    path           TEXT NOT NULL,                     -- absolute path on that daemon where the clone lives
    default_branch TEXT,
    cloned_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, daemon_id)
);

CREATE INDEX project_daemons_daemon_idx ON project_daemons(daemon_id);

-- +goose Down
DROP INDEX IF EXISTS project_daemons_daemon_idx;
DROP TABLE IF EXISTS project_daemons;
DROP INDEX IF EXISTS projects_user_remote_url_uniq;
ALTER TABLE projects DROP COLUMN remote_url;
