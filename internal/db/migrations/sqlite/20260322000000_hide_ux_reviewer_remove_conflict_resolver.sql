-- +goose Up
-- +goose StatementBegin

-- Hide ux_reviewer: specialist preset spawned by code_reviewer
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-ux-reviewer',
    2,
    'ux_reviewer',
    true,
    'Specialist preset spawned by code_reviewer'
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true;

-- Remove conflict-resolver default: preset has been removed (replaced by builtin skill)
DELETE FROM item_defaults WHERE id = 'default-preset-conflict-resolver';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DELETE FROM item_defaults WHERE id = 'default-preset-ux-reviewer';

INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-conflict-resolver',
    2,
    'conflict-resolver',
    true,
    'Specialist preset spawned by git preset'
) ON CONFLICT (item_type, slug) DO NOTHING;

-- +goose StatementEnd
