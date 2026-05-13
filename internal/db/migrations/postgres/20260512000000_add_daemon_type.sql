-- +goose Up
ALTER TABLE daemons ADD COLUMN daemon_type TEXT;

-- +goose Down
ALTER TABLE daemons DROP COLUMN daemon_type;
