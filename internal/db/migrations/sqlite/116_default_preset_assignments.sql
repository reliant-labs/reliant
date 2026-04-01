-- +goose Up
-- +goose StatementBegin

-- Default preset assignments for workflows.
-- Stores which preset should be used by default for a workflow's inputs/groups.
CREATE TABLE default_preset_assignments (
    id TEXT PRIMARY KEY,
    -- The workflow name (e.g., 'builtin://agent')
    workflow_name TEXT NOT NULL,
    -- The group name (empty string "" for top-level/workflow-level inputs)
    group_name TEXT NOT NULL DEFAULT '',
    -- The preset slug to use as default
    preset_slug TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workflow_name, group_name)
);

CREATE INDEX idx_default_preset_assignments_workflow ON default_preset_assignments(workflow_name);

-- Seed default preset assignments for builtin workflows

-- builtin://agent: uses 'general' preset for top-level inputs
INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-agent-toplevel', 'builtin://agent', '', 'general');

-- builtin://plan-debate: per-group defaults
INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-plan-debate-planner', 'builtin://plan-debate', 'Planner', 'researcher');

INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-plan-debate-critic', 'builtin://plan-debate', 'Critic', 'researcher');

INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-plan-debate-implementer', 'builtin://plan-debate', 'Implementer', 'general');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_default_preset_assignments_workflow;
DROP TABLE IF EXISTS default_preset_assignments;

-- +goose StatementEnd
