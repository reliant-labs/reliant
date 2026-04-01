-- +goose Up
ALTER TABLE project_configs ADD COLUMN project_workflows_json TEXT;
ALTER TABLE project_configs ADD COLUMN project_presets_json TEXT;
ALTER TABLE project_configs ADD COLUMN project_scenarios_json TEXT;

-- +goose Down
ALTER TABLE project_configs DROP COLUMN project_scenarios_json;
ALTER TABLE project_configs DROP COLUMN project_presets_json;
ALTER TABLE project_configs DROP COLUMN project_workflows_json;
