-- +goose Up
-- +goose StatementBegin
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-security-reviewer',
    'preset',
    'security_reviewer',
    true,
    'Specialist preset spawned by code_reviewer'
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true;

INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-architect',
    'preset',
    'architect',
    true,
    'Specialist preset spawned by code_reviewer'
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true;

INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-performance-reviewer',
    'preset',
    'performance_reviewer',
    true,
    'Specialist preset spawned by code_reviewer'
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true;

INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason)
VALUES (
    'default-preset-code-hygiene-reviewer',
    'preset',
    'code_hygiene_reviewer',
    true,
    'Specialist preset spawned by code_reviewer'
) ON CONFLICT (item_type, slug) DO UPDATE SET is_hidden = true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM item_defaults WHERE id = 'default-preset-security-reviewer';
DELETE FROM item_defaults WHERE id = 'default-preset-architect';
DELETE FROM item_defaults WHERE id = 'default-preset-performance-reviewer';
DELETE FROM item_defaults WHERE id = 'default-preset-code-hygiene-reviewer';
-- +goose StatementEnd
