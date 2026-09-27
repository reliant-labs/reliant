-- +goose Up
-- +goose StatementBegin

-- Hide ux_reviewer: specialist preset spawned by code_reviewer
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason, created_at, updated_at)
VALUES (
    'default-preset-ux-reviewer',
    2,
    'ux_reviewer',
    true,
    'Specialist preset spawned by code_reviewer',
    NOW(),
    NOW()
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true, updated_at = NOW();

-- Remove conflict-resolver default: preset has been removed (replaced by builtin skill)
DELETE FROM item_defaults WHERE id = 'default-preset-conflict-resolver';

-- +goose StatementEnd

