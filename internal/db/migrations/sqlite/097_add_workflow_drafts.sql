-- +goose Up
-- Workflow drafts table for storing workflow definitions during editing
-- Drafts can be invalid (validation happens on publish)
CREATE TABLE workflow_drafts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,           -- JSON workflow definition
    is_valid INTEGER NOT NULL DEFAULT 0, -- Boolean: passes validation
    validation_errors TEXT,              -- JSON array of validation errors
    source_path TEXT,                    -- Original file path if imported from filesystem
    published_at DATETIME,               -- NULL = draft, set = published to filesystem
    published_path TEXT,                 -- Path where it was published
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

-- Index for listing drafts by project
CREATE INDEX idx_workflow_drafts_project ON workflow_drafts(project_id, user_id);

-- Index for finding draft by source path (for re-importing)
CREATE INDEX idx_workflow_drafts_source ON workflow_drafts(project_id, source_path);

-- Trigger to update updated_at on changes
CREATE TRIGGER update_workflow_drafts_timestamp AFTER UPDATE ON workflow_drafts BEGIN UPDATE workflow_drafts SET updated_at = datetime('now', 'utc') WHERE id = NEW.id; END;

-- +goose Down
DROP TRIGGER IF EXISTS update_workflow_drafts_timestamp;
DROP INDEX IF EXISTS idx_workflow_drafts_source;
DROP INDEX IF EXISTS idx_workflow_drafts_project;
DROP TABLE IF EXISTS workflow_drafts;
