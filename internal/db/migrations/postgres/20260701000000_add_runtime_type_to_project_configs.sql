-- +goose Up
ALTER TABLE project_configs ADD COLUMN IF NOT EXISTS runtime_type TEXT;

-- +goose Down
ALTER TABLE project_configs DROP COLUMN IF EXISTS runtime_type;
