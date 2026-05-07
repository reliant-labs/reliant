-- +goose Up
-- Add Repo entity: a project may contain N nested git repos.
-- Single-repo projects get one repo row with relative_path = ''.
--
-- A Worktree is a workspace-level entity (one row per feature, regardless of
-- repo count). It does NOT carry a repo_id — its Path points at a workspace
-- directory that contains N nested checkouts, one per repo. See
-- internal/db/core/project_worktree.go for the rationale.

CREATE TABLE repos (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    relative_path TEXT NOT NULL DEFAULT '',
    remote_url TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    UNIQUE(project_id, relative_path)
);

CREATE INDEX idx_repos_project ON repos(project_id);

-- Backfill: one root-level repo per existing git project.
INSERT INTO repos (id, project_id, name, relative_path, created_at, updated_at)
SELECT
    lower(hex(randomblob(16))),
    p.id,
    p.name,
    '',
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
FROM projects p
WHERE p.is_git_repo = TRUE
  AND NOT EXISTS (SELECT 1 FROM repos r WHERE r.project_id = p.id);

-- +goose Down
DROP INDEX IF EXISTS idx_repos_project;
DROP TABLE IF EXISTS repos;
