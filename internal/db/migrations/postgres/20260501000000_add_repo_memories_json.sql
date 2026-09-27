-- +goose Up
ALTER TABLE project_configs ADD COLUMN IF NOT EXISTS repo_memories_json TEXT;

