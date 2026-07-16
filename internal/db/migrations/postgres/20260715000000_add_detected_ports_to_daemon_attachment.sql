-- +goose Up
-- Detected listener ports reported by the daemon heartbeat (loopback/wildcard
-- LISTEN sockets inside the workspace netns — what the in-pod preview
-- forwarder can reach). JSON-encoded int array. Lives on the attachment
-- (liveness) record like the memory telemetry: it disappears with the row on
-- disconnect, since ports from a dead daemon are meaningless.
ALTER TABLE daemon_attachment ADD COLUMN detected_ports TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE daemon_attachment DROP COLUMN detected_ports;
