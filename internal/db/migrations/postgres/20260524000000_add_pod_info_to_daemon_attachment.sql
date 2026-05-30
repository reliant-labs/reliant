-- +goose Up
ALTER TABLE daemon_attachment ADD COLUMN pod_ip TEXT;
ALTER TABLE daemon_attachment ADD COLUMN pod_port INTEGER;

-- +goose Down
ALTER TABLE daemon_attachment DROP COLUMN pod_port;
ALTER TABLE daemon_attachment DROP COLUMN pod_ip;
