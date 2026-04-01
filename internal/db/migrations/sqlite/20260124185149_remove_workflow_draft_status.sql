-- +goose Up
-- Remove published/draft status concept from workflow_drafts.
-- Workflows are now usable when is_hidden=0 AND is_valid=1.

-- Drop indexes that reference status
DROP INDEX IF EXISTS idx_workflow_drafts_user;
DROP INDEX IF EXISTS idx_workflow_drafts_slug;

-- SQLite doesn't support DROP COLUMN cleanly, so recreate the table
CREATE TABLE workflow_drafts_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,
    is_valid INTEGER NOT NULL DEFAULT 0,
    validation_errors TEXT,
    source_path TEXT,
    forked_from TEXT,
    is_hidden BOOLEAN NOT NULL DEFAULT 0,
    chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO workflow_drafts_new (id, user_id, name, slug, description, definition, is_valid, validation_errors, source_path, forked_from, is_hidden, chat_id, created_at, updated_at)
SELECT id, user_id, name, slug, description, definition, is_valid, validation_errors, source_path, forked_from, is_hidden, chat_id, created_at, updated_at
FROM workflow_drafts;

DROP TABLE workflow_drafts;
ALTER TABLE workflow_drafts_new RENAME TO workflow_drafts;

-- Recreate indexes (without status references)
CREATE UNIQUE INDEX idx_workflow_drafts_unique_slug ON workflow_drafts(user_id, slug);
CREATE INDEX idx_workflow_drafts_user ON workflow_drafts(user_id);
CREATE INDEX idx_workflow_drafts_slug ON workflow_drafts(slug);
CREATE INDEX idx_workflow_drafts_chat ON workflow_drafts(chat_id);
CREATE INDEX idx_workflow_drafts_forked_from ON workflow_drafts(forked_from);
CREATE INDEX idx_workflow_drafts_is_hidden ON workflow_drafts(is_hidden);

-- Recreate the timestamp trigger
-- +goose StatementBegin
CREATE TRIGGER update_workflow_drafts_timestamp
AFTER UPDATE ON workflow_drafts
BEGIN
    UPDATE workflow_drafts SET updated_at = datetime('now', 'utc') WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
-- Add back status and published_at columns
ALTER TABLE workflow_drafts ADD COLUMN status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published'));
ALTER TABLE workflow_drafts ADD COLUMN published_at DATETIME;
