-- +goose Up
ALTER TABLE project_configs ADD COLUMN project_skills_json TEXT;

