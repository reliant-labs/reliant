-- +goose Up
-- Add chat_id to workflow_drafts for implicit draft lookup from chat context
ALTER TABLE workflow_drafts ADD COLUMN chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL;

-- Index for looking up draft by chat
CREATE INDEX idx_workflow_drafts_chat ON workflow_drafts(chat_id);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_drafts_chat;
-- SQLite doesn't support DROP COLUMN, so we recreate the table
CREATE TABLE workflow_drafts_new (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,
    is_valid INTEGER NOT NULL DEFAULT 0,
    validation_errors TEXT,
    source_path TEXT,
    published_at DATETIME,
    published_path TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

INSERT INTO workflow_drafts_new (id, project_id, user_id, name, description, definition, is_valid, validation_errors, source_path, published_at, published_path, created_at, updated_at)
SELECT id, project_id, user_id, name, description, definition, is_valid, validation_errors, source_path, published_at, published_path, created_at, updated_at
FROM workflow_drafts;

DROP TABLE workflow_drafts;
ALTER TABLE workflow_drafts_new RENAME TO workflow_drafts;

CREATE INDEX idx_workflow_drafts_project ON workflow_drafts(project_id, user_id);
CREATE INDEX idx_workflow_drafts_source ON workflow_drafts(project_id, source_path);
CREATE TRIGGER update_workflow_drafts_timestamp AFTER UPDATE ON workflow_drafts BEGIN UPDATE workflow_drafts SET updated_at = datetime('now', 'utc') WHERE id = NEW.id; END;
