-- +goose Up
-- +goose StatementBegin

-- Seed default visibility for builtin presets.
-- These are "specialist" presets that are spawned by other presets,
-- not intended to be selected directly by users.

-- conflict-resolver: Spawned by git preset for complex merge conflicts
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-conflict-resolver',
    'preset',
    'conflict-resolver',
    true,
    'Specialist preset spawned by git preset'
);

-- reproducer: Spawned by debug preset for runtime reproduction
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-reproducer',
    'preset',
    'reproducer',
    true,
    'Specialist preset spawned by debug preset'
);

-- refactor: Spawned by planner/general for focused refactoring work
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-refactor',
    'preset',
    'refactor',
    true,
    'Specialist preset spawned by planner or general'
);

-- Seed default visibility for builtin workflows that should be hidden.

-- presets-demo: Example/demo workflow, not for regular use
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-workflow-presets-demo',
    'workflow',
    'builtin://presets-demo',
    true,
    'Example workflow for demonstration purposes'
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DELETE FROM item_defaults WHERE id IN (
    'default-preset-conflict-resolver',
    'default-preset-reproducer',
    'default-preset-refactor',
    'default-workflow-presets-demo'
);

-- +goose StatementEnd
