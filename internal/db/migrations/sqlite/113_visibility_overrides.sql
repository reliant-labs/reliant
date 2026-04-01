-- +goose Up
-- +goose StatementBegin

-- Drop old hidden_items table (no data migration)
DROP INDEX IF EXISTS idx_hidden_items_type;
DROP INDEX IF EXISTS idx_hidden_items_user_id;
DROP TABLE IF EXISTS hidden_items;

-- Visibility overrides table for user-specific visibility preferences.
-- Users can SHOW items that are hidden by default, or HIDE items that are visible.
CREATE TABLE visibility_overrides (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    item_type TEXT NOT NULL CHECK (item_type IN ('workflow', 'preset')),
    slug TEXT NOT NULL,
    is_visible BOOLEAN NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, item_type, slug)
);

CREATE INDEX idx_visibility_overrides_user_id ON visibility_overrides(user_id);
CREATE INDEX idx_visibility_overrides_type ON visibility_overrides(user_id, item_type);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_visibility_overrides_type;
DROP INDEX IF EXISTS idx_visibility_overrides_user_id;
DROP TABLE IF EXISTS visibility_overrides;

-- +goose StatementEnd
