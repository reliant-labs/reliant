-- +goose Up
ALTER TABLE project_configs ADD COLUMN IF NOT EXISTS repo_memories_json TEXT;

-- +goose Down
ALTER TABLE project_configs DROP COLUMN IF EXISTS repo_memories_json;
