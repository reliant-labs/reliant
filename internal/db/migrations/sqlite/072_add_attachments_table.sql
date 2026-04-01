-- Migration: Add attachments table to persist attachment metadata
-- This allows us to store and retrieve real attachment information instead of using placeholders

-- +goose Up
CREATE TABLE IF NOT EXISTS attachments (
    id TEXT PRIMARY KEY,              -- Attachment UUID
    user_id TEXT NOT NULL,            -- User who uploaded the attachment
    filename TEXT NOT NULL,           -- Original filename
    size INTEGER NOT NULL,            -- File size in bytes
    mime_type TEXT NOT NULL,          -- MIME type (e.g., image/jpeg, application/pdf)
    file_hash TEXT,                   -- SHA-256 hash of file content
    file_path TEXT NOT NULL,          -- Relative path to file in uploads directory
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index for faster lookups by user
CREATE INDEX IF NOT EXISTS idx_attachments_user_id ON attachments(user_id);

-- Index for faster lookups by hash (deduplication)
CREATE INDEX IF NOT EXISTS idx_attachments_file_hash ON attachments(file_hash);

-- Trigger to update updated_at timestamp
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS update_attachments_timestamp 
    AFTER UPDATE ON attachments
    FOR EACH ROW
BEGIN
    UPDATE attachments SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS update_attachments_timestamp;
DROP INDEX IF EXISTS idx_attachments_file_hash;
DROP INDEX IF EXISTS idx_attachments_user_id;
DROP TABLE IF EXISTS attachments;
