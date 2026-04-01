-- +goose Up
-- +goose StatementBegin

-- Command favorites table for storing user-favorited package commands per project.
-- This replaces the frontend localStorage-based storage with proper backend persistence.
CREATE TABLE command_favorites (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    -- The command key in format: {package_type}:{name} or {package_type}:{relative_path}:{name}
    -- Examples: 'npm:dev', 'makefile:build', 'npm:electron:start'
    command_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    -- Each user can only favorite a command once per project
    UNIQUE(user_id, project_id, command_key)
);

CREATE INDEX idx_command_favorites_user_project ON command_favorites(user_id, project_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_command_favorites_user_project;
DROP TABLE IF EXISTS command_favorites;

-- +goose StatementEnd
