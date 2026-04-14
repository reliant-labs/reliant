-- +goose Up
-- SQLite doesn't support ALTER TABLE ... DROP/ADD CONSTRAINT,
-- so we must recreate the table to update the CHECK constraint.

CREATE TABLE attachments_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    size INTEGER NOT NULL,
    mime_type TEXT NOT NULL,
    file_hash TEXT,
    file_path TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attachment_type TEXT NOT NULL DEFAULT 'image' CHECK (attachment_type IN ('image', 'file_reference', 'document')),
    content BLOB
);

INSERT INTO attachments_new (id, user_id, filename, size, mime_type, file_hash, file_path, created_at, updated_at, attachment_type, content)
    SELECT id, user_id, filename, size, mime_type, file_hash, file_path, created_at, updated_at, attachment_type, content
    FROM attachments;

DROP TABLE attachments;

ALTER TABLE attachments_new RENAME TO attachments;

CREATE INDEX idx_attachments_user_id ON attachments(user_id);
CREATE INDEX idx_attachments_file_hash ON attachments(file_hash);

-- +goose StatementBegin
CREATE TRIGGER update_attachments_timestamp
    AFTER UPDATE ON attachments
    FOR EACH ROW
BEGIN
    UPDATE attachments SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
CREATE TABLE attachments_old (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    size INTEGER NOT NULL,
    mime_type TEXT NOT NULL,
    file_hash TEXT,
    file_path TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attachment_type TEXT NOT NULL DEFAULT 'image' CHECK (attachment_type IN ('image', 'file_reference')),
    content BLOB
);

INSERT INTO attachments_old (id, user_id, filename, size, mime_type, file_hash, file_path, created_at, updated_at, attachment_type, content)
    SELECT id, user_id, filename, size, mime_type, file_hash, file_path, created_at, updated_at, attachment_type, content
    FROM attachments;

DROP TABLE attachments;

ALTER TABLE attachments_old RENAME TO attachments;

CREATE INDEX idx_attachments_user_id ON attachments(user_id);
CREATE INDEX idx_attachments_file_hash ON attachments(file_hash);

-- +goose StatementBegin
CREATE TRIGGER update_attachments_timestamp
    AFTER UPDATE ON attachments
    FOR EACH ROW
BEGIN
    UPDATE attachments SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;
-- +goose StatementEnd
