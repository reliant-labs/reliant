-- +goose Up
ALTER TABLE project_configs ADD COLUMN project_skills_json TEXT;

-- +goose Down
ALTER TABLE project_configs DROP COLUMN project_skills_json;
