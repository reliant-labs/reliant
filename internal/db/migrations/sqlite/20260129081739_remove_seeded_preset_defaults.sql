-- +goose Up
-- +goose StatementBegin

-- Remove seeded default preset assignments.
-- Defaults are now defined in workflow YAML files (presets.default field).
-- The table structure is kept for potential user override functionality.
DELETE FROM default_preset_assignments;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Re-seed the defaults (from 116_default_preset_assignments.sql and fixes)
INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-agent-toplevel', 'builtin://agent', '', 'general');

INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-plan-debate-implementer-fixed', 'builtin://plan-debate', 'implementer', 'general');

-- +goose StatementEnd
