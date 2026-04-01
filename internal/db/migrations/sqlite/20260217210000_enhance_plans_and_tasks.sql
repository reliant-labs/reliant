-- +goose Up

-- 1. Add complexity column to plans
ALTER TABLE plans ADD COLUMN complexity TEXT CHECK (complexity IN ('simple', 'moderate', 'complex'));

-- 2. Add metadata columns to tasks and fix status CHECK to include 'skipped'
-- SQLite cannot ALTER CHECK constraints, so we recreate the table
ALTER TABLE tasks RENAME TO tasks_old;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    parent_task_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending',
        'in_progress',
        'completed',
        'failed',
        'blocked',
        'cancelled',
        'skipped'
    )),
    position INTEGER NOT NULL DEFAULT 0,
    metadata TEXT,
    assignee TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

INSERT INTO tasks (id, plan_id, parent_task_id, title, description, status, position, created_at, updated_at, completed_at)
    SELECT id, plan_id, parent_task_id, title, description, status, position, created_at, updated_at, completed_at
    FROM tasks_old;

DROP TABLE tasks_old;

-- 3. Create task_dependencies table for dependency graph
CREATE TABLE task_dependencies (
    id TEXT PRIMARY KEY,
    from_task_id TEXT NOT NULL,
    to_task_id TEXT NOT NULL,
    dependency_type TEXT NOT NULL CHECK (dependency_type IN (
        'blocks',          -- from_task blocks to_task (to_task cannot start until from_task completes)
        'related',         -- informational link, no execution constraint
        'parallel_with'    -- explicitly parallelizable
    )),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (from_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (to_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(from_task_id, to_task_id, dependency_type)
);

CREATE INDEX idx_task_deps_from ON task_dependencies(from_task_id);
CREATE INDEX idx_task_deps_to ON task_dependencies(to_task_id);
CREATE INDEX idx_task_deps_type ON task_dependencies(dependency_type);

-- 4. Add project_id to plans for cross-workflow sharing
ALTER TABLE plans ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE CASCADE;

CREATE INDEX idx_plans_project ON plans(project_id) WHERE project_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_plans_project;
DROP TABLE IF EXISTS task_dependencies;
-- SQLite <3.35 cannot DROP COLUMN, best-effort
SELECT 1;
