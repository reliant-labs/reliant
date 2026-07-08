-- +goose Up
ALTER TABLE project_configs ADD COLUMN runtime_type TEXT;

-- +goose Down
ALTER TABLE project_configs DROP COLUMN runtime_type;
