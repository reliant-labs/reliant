-- +goose Up
-- Workspace memory telemetry reported by the daemon heartbeat (cloud daemons
-- running in a cgroup-limited pod). Lives on the attachment (liveness) record
-- so it disappears with the row on disconnect — stale readings from a dead
-- daemon are meaningless. memory_limit_bytes = 0 means "not reported"
-- (local daemons without cgroup accounting).
ALTER TABLE daemon_attachment ADD COLUMN memory_used_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daemon_attachment ADD COLUMN memory_limit_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daemon_attachment ADD COLUMN memory_pressure BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE daemon_attachment DROP COLUMN memory_pressure;
ALTER TABLE daemon_attachment DROP COLUMN memory_limit_bytes;
ALTER TABLE daemon_attachment DROP COLUMN memory_used_bytes;
