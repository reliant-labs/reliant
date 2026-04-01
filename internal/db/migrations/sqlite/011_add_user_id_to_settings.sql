-- Add user_id to settings table for multi-user support
-- No backwards compatibility - all existing settings get a default user_id

-- +goose Up
-- SQLite doesn't allow altering UNIQUE constraints, so we need to recreate the table
CREATE TABLE settings_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL DEFAULT '4630f1da-5058-4707-804b-aa30362f3e40',
    project_id TEXT,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    value_type TEXT NOT NULL CHECK (value_type IN ('string', 'int', 'float', 'bool')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    UNIQUE(user_id, project_id, key)
);

-- Copy existing data (all get default user_id)
INSERT INTO settings_new (id, user_id, project_id, key, value, value_type, created_at, updated_at)
SELECT id, '4630f1da-5058-4707-804b-aa30362f3e40', project_id, key, value, value_type, created_at, updated_at
FROM settings;

-- Drop old table
DROP TABLE settings;

-- Rename new table
ALTER TABLE settings_new RENAME TO settings;

-- Create indexes
CREATE INDEX idx_settings_user_id ON settings(user_id);
CREATE INDEX idx_settings_project ON settings(project_id);
CREATE INDEX idx_settings_key ON settings(key);

-- +goose Down
-- Recreate original table without user_id
CREATE TABLE settings_old (
    id TEXT PRIMARY KEY,
    project_id TEXT,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    value_type TEXT NOT NULL CHECK (value_type IN ('string', 'int', 'float', 'bool')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    UNIQUE(project_id, key)
);

-- Copy data back (losing user_id)
INSERT INTO settings_old (id, project_id, key, value, value_type, created_at, updated_at)
SELECT id, project_id, key, value, value_type, created_at, updated_at
FROM settings;

-- Drop new table
DROP TABLE settings;

-- Rename old table
ALTER TABLE settings_old RENAME TO settings;

-- Recreate indexes
CREATE INDEX idx_settings_project ON settings(project_id);
CREATE INDEX idx_settings_key ON settings(key);
