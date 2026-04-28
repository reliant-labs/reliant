-- +goose Up
-- Add Repo entity: a project may contain N nested git repos.
-- Single-repo projects get one repo row with relative_path = ''.

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

-- repo_id is nullable during the transition. New worktrees set it explicitly.
-- Existing worktrees are backfilled below; nulls remain only for projects with
-- is_git_repo = FALSE.
ALTER TABLE worktrees ADD COLUMN repo_id TEXT REFERENCES repos(id) ON DELETE CASCADE;

CREATE INDEX idx_worktrees_repo ON worktrees(repo_id);

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

-- Backfill existing worktrees to point to their project's root-level repo.
UPDATE worktrees
SET repo_id = (SELECT id FROM repos WHERE project_id = worktrees.project_id LIMIT 1)
WHERE repo_id IS NULL
  AND project_id IN (SELECT project_id FROM repos);

-- +goose Down
DROP INDEX IF EXISTS idx_worktrees_repo;
ALTER TABLE worktrees DROP COLUMN repo_id;
DROP INDEX IF EXISTS idx_repos_project;
DROP TABLE IF EXISTS repos;
