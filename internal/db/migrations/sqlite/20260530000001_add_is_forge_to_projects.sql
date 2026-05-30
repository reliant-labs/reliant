-- +goose Up
ALTER TABLE projects ADD COLUMN is_forge BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE projects DROP COLUMN is_forge;
