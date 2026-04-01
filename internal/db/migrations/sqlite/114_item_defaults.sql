-- +goose Up
-- +goose StatementBegin

-- Item defaults table stores the builtin default visibility for presets and workflows.
-- This is seeded by migrations and represents "factory defaults".
-- Users can override these via visibility_overrides table.
CREATE TABLE item_defaults (
    id TEXT PRIMARY KEY,
    -- 'workflow' or 'preset'
    item_type TEXT NOT NULL CHECK (item_type IN ('workflow', 'preset')),
    -- The slug/name of the item
    slug TEXT NOT NULL,
    -- true = hidden by default (specialist presets, internal workflows)
    is_hidden BOOLEAN NOT NULL DEFAULT FALSE,
    -- Human-readable reason for the default (optional, for documentation)
    reason TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Each item can only have one default
    UNIQUE(item_type, slug)
);

CREATE INDEX idx_item_defaults_type ON item_defaults(item_type);
CREATE INDEX idx_item_defaults_hidden ON item_defaults(item_type, is_hidden);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_item_defaults_hidden;
DROP INDEX IF EXISTS idx_item_defaults_type;
DROP TABLE IF EXISTS item_defaults;

-- +goose StatementEnd
