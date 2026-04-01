-- +goose Up
-- +goose StatementBegin

-- Hidden items table for tracking user-hidden workflows and presets.
-- This provides a proper schema instead of storing JSON in the settings table.
CREATE TABLE hidden_items (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    -- 'workflow' or 'preset'
    item_type TEXT NOT NULL CHECK (item_type IN ('workflow', 'preset')),
    -- The slug/name of the hidden item (e.g., 'general', 'builtin://agent')
    slug TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- Each user can only hide an item once
    UNIQUE(user_id, item_type, slug)
);

CREATE INDEX idx_hidden_items_user_id ON hidden_items(user_id);
CREATE INDEX idx_hidden_items_type ON hidden_items(user_id, item_type);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_hidden_items_type;
DROP INDEX IF EXISTS idx_hidden_items_user_id;
DROP TABLE IF EXISTS hidden_items;

-- +goose StatementEnd
