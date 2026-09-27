-- +goose Up
DROP INDEX IF EXISTS idx_daemons_status;
ALTER TABLE daemons DROP COLUMN status;
ALTER TABLE daemons DROP COLUMN connected_at;
ALTER TABLE daemons DROP COLUMN last_heartbeat;
ALTER TABLE daemons DROP COLUMN disconnected_at;

