-- +goose Up
-- +goose StatementBegin

-- User-created presets stored in the database.
-- These complement file-based presets from .reliant/presets/ directories.
-- User presets take precedence over file-based presets with the same slug.
CREATE TABLE presets (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    -- NULL = global (user-wide), non-null = project-specific
    project_id TEXT,
    -- Display name for the preset
    name TEXT NOT NULL,
    -- URL-safe identifier for runtime reference
    slug TEXT NOT NULL,
    -- Human-readable description
    description TEXT,
    -- Tag declaring which workflow/group inputs this preset targets
    tag TEXT NOT NULL,
    -- JSON object of parameter name -> value
    params TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    -- Unique slug per user per scope (global or project)
    UNIQUE(user_id, project_id, slug)
);

CREATE INDEX idx_presets_user_id ON presets(user_id);
CREATE INDEX idx_presets_project ON presets(project_id);
CREATE INDEX idx_presets_tag ON presets(tag);
CREATE INDEX idx_presets_slug ON presets(user_id, slug);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_presets_slug;
DROP INDEX IF EXISTS idx_presets_tag;
DROP INDEX IF EXISTS idx_presets_project;
DROP INDEX IF EXISTS idx_presets_user_id;
DROP TABLE IF EXISTS presets;

-- +goose StatementEnd
