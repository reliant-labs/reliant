-- +goose Up
DROP INDEX IF EXISTS idx_daemons_status;
ALTER TABLE daemons DROP COLUMN status;
ALTER TABLE daemons DROP COLUMN connected_at;
ALTER TABLE daemons DROP COLUMN last_heartbeat;
ALTER TABLE daemons DROP COLUMN disconnected_at;

-- +goose Down
ALTER TABLE daemons ADD COLUMN disconnected_at TIMESTAMP;
ALTER TABLE daemons ADD COLUMN last_heartbeat TIMESTAMP;
ALTER TABLE daemons ADD COLUMN connected_at TIMESTAMP;
ALTER TABLE daemons ADD COLUMN status INTEGER NOT NULL DEFAULT 3;
CREATE INDEX IF NOT EXISTS idx_daemons_status ON daemons(status);
