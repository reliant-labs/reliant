-- +goose Up
ALTER TABLE project_configs ADD COLUMN repo_memories_json TEXT;

-- +goose Down
ALTER TABLE project_configs DROP COLUMN repo_memories_json;
