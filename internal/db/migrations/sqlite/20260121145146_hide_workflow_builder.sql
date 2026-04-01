-- +goose Up
-- +goose StatementBegin

-- Hide workflow-builder from the workflow dropdown by default.
-- This is a development/debugging workflow not intended for regular use.
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-workflow-workflow-builder',
    'workflow',
    'builtin://workflow-builder',
    true,
    'Development workflow for building workflows'
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DELETE FROM item_defaults WHERE id = 'default-workflow-workflow-builder';

-- +goose StatementEnd
