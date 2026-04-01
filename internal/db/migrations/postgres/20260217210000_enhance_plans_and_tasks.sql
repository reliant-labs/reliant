-- +goose Up

-- 1. Add complexity column to plans
ALTER TABLE plans ADD COLUMN complexity TEXT;

-- 2. Add metadata columns to tasks
ALTER TABLE tasks ADD COLUMN metadata TEXT; -- JSON: preferred_agent, tool_hints, dependencies, notes, priority
ALTER TABLE tasks ADD COLUMN assignee TEXT;

-- 3. Create task_dependencies table for dependency graph
CREATE TABLE task_dependencies (
    id TEXT PRIMARY KEY,
    from_task_id TEXT NOT NULL,
    to_task_id TEXT NOT NULL,
    dependency_type TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),

    FOREIGN KEY (from_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (to_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(from_task_id, to_task_id, dependency_type),
    CHECK (dependency_type IN ('blocks', 'related', 'parallel_with'))
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
ALTER TABLE plans DROP COLUMN IF EXISTS project_id;
ALTER TABLE plans DROP COLUMN IF EXISTS complexity;
ALTER TABLE tasks DROP COLUMN IF EXISTS assignee;
ALTER TABLE tasks DROP COLUMN IF EXISTS metadata;
