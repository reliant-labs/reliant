-- +goose Up
CREATE TABLE IF NOT EXISTS daemon_attachment (
    daemon_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('inbound', 'outbound')),
    attached_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_stream_activity TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_daemon_attachment_user_id ON daemon_attachment(user_id);
CREATE INDEX IF NOT EXISTS idx_daemon_attachment_last_activity ON daemon_attachment(last_stream_activity);

-- +goose Down
DROP INDEX IF EXISTS idx_daemon_attachment_last_activity;
DROP INDEX IF EXISTS idx_daemon_attachment_user_id;
DROP TABLE IF EXISTS daemon_attachment;
