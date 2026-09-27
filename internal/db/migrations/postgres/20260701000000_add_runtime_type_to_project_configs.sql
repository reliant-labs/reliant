-- +goose Up
ALTER TABLE project_configs ADD COLUMN IF NOT EXISTS runtime_type TEXT;

