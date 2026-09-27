-- +goose Up
ALTER TABLE daemon_attachment ADD COLUMN pod_ip TEXT;
ALTER TABLE daemon_attachment ADD COLUMN pod_port INTEGER;

