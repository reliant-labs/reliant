-- +goose Up

ALTER TABLE project_configs
    ADD COLUMN IF NOT EXISTS global_memory_md TEXT;

ALTER TABLE project_configs
    ADD COLUMN IF NOT EXISTS project_memory_md TEXT;

