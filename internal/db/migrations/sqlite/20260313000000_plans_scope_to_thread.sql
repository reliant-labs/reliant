-- +goose Up
-- +goose NO TRANSACTION

-- Disable FK checks during table rebuild
PRAGMA foreign_keys = OFF;

-- Replace chat_id with thread_id on plans table for thread-scoped plans.
-- SQLite can't DROP COLUMN when it's in a FK, so we recreate the table.
-- We must also recreate tasks and task_dependencies because SQLite binds
-- FK references to the original table object, not by name.
ALTER TABLE plans RENAME TO plans_old;

CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    complexity INTEGER,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE
);

INSERT INTO plans (id, thread_id, title, description, status, created_at, updated_at, completed_at, complexity, project_id)
    SELECT id, chat_id, title, description, status, created_at, updated_at, completed_at, complexity, project_id
    FROM plans_old;

DROP TABLE plans_old;

-- Recreate tasks table to fix FK reference to new plans table
ALTER TABLE tasks RENAME TO tasks_old;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    parent_task_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    position INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    metadata TEXT,
    assignee TEXT,
    FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

INSERT INTO tasks (id, plan_id, parent_task_id, title, description, status, position, created_at, updated_at, completed_at, metadata, assignee)
    SELECT id, plan_id, parent_task_id, title, description, status, COALESCE(position, 0), created_at, updated_at, completed_at, metadata, assignee
    FROM tasks_old;
DROP TABLE tasks_old;

-- Recreate task_dependencies to fix FK references to new tasks table
ALTER TABLE task_dependencies RENAME TO task_dependencies_old;

CREATE TABLE task_dependencies (
    id TEXT PRIMARY KEY,
    from_task_id TEXT NOT NULL,
    to_task_id TEXT NOT NULL,
    dependency_type INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (from_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (to_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(from_task_id, to_task_id, dependency_type)
);

INSERT INTO task_dependencies SELECT * FROM task_dependencies_old;
DROP TABLE task_dependencies_old;

-- Recreate indexes
CREATE INDEX idx_plans_thread ON plans(thread_id);
CREATE INDEX idx_plans_status ON plans(status);
CREATE INDEX idx_plans_project ON plans(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX idx_tasks_plan ON tasks(plan_id, position);
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_task_deps_from ON task_dependencies(from_task_id);
CREATE INDEX idx_task_deps_to ON task_dependencies(to_task_id);
CREATE INDEX idx_task_deps_type ON task_dependencies(dependency_type);

-- Re-enable FK checks
PRAGMA foreign_keys = ON;

-- +goose Down
-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

ALTER TABLE plans RENAME TO plans_old;

CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    complexity INTEGER,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

INSERT INTO plans (id, chat_id, title, description, status, created_at, updated_at, completed_at, complexity, project_id)
    SELECT id, thread_id, title, description, status, created_at, updated_at, completed_at, complexity, project_id
    FROM plans_old;

DROP TABLE plans_old;

CREATE INDEX idx_plans_chat ON plans(chat_id);
CREATE INDEX idx_plans_status ON plans(status);
CREATE INDEX idx_plans_project ON plans(project_id) WHERE project_id IS NOT NULL;

PRAGMA foreign_keys = ON;
