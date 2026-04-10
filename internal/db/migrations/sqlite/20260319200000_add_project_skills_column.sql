-- +goose Up
ALTER TABLE project_configs ADD COLUMN project_skills_json TEXT;

-- +goose Down
-- sqlite does not support DROP COLUMN on this codebase; forward-only.
