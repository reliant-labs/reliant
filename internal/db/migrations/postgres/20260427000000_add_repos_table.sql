-- +goose Up
-- Add Repo entity: a project may contain N nested git repos.
-- Single-repo projects get one repo row with relative_path = ''.

CREATE TABLE IF NOT EXISTS repos (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    relative_path TEXT NOT NULL DEFAULT '',
    remote_url TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, relative_path)
);

CREATE INDEX IF NOT EXISTS idx_repos_project ON repos(project_id);

ALTER TABLE worktrees ADD COLUMN IF NOT EXISTS repo_id TEXT REFERENCES repos(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_worktrees_repo ON worktrees(repo_id);

-- Backfill: one root-level repo per existing git project.
INSERT INTO repos (id, project_id, name, relative_path, created_at, updated_at)
SELECT
    gen_random_uuid()::text,
    p.id,
    p.name,
    '',
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
FROM projects p
WHERE p.is_git_repo = TRUE
  AND NOT EXISTS (SELECT 1 FROM repos r WHERE r.project_id = p.id);

UPDATE worktrees w
SET repo_id = r.id
FROM repos r
WHERE r.project_id = w.project_id
  AND w.repo_id IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_worktrees_repo;
ALTER TABLE worktrees DROP COLUMN IF EXISTS repo_id;
DROP INDEX IF EXISTS idx_repos_project;
DROP TABLE IF EXISTS repos;
