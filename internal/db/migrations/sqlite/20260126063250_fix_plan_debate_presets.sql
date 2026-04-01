-- +goose Up
-- +goose StatementBegin

-- Fix plan-debate preset assignments.
-- The plan-debate workflow only has 'implementer' as an input group.
-- 'Planner' and 'Critic' are node IDs within the inline loop, not input groups.
-- Also fix the case: 'Implementer' -> 'implementer'

-- Delete invalid preset assignments for non-existent groups
DELETE FROM default_preset_assignments
WHERE workflow_name = 'builtin://plan-debate'
  AND group_name IN ('Planner', 'Critic');

-- Fix case of Implementer -> implementer
UPDATE default_preset_assignments
SET id = 'default-plan-debate-implementer-fixed',
    group_name = 'implementer'
WHERE workflow_name = 'builtin://plan-debate'
  AND group_name = 'Implementer';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Restore original (incorrect) preset assignments
INSERT OR REPLACE INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-plan-debate-planner', 'builtin://plan-debate', 'Planner', 'researcher');

INSERT OR REPLACE INTO default_preset_assignments (id, workflow_name, group_name, preset_slug)
VALUES ('default-plan-debate-critic', 'builtin://plan-debate', 'Critic', 'researcher');

UPDATE default_preset_assignments
SET id = 'default-plan-debate-implementer',
    group_name = 'Implementer'
WHERE workflow_name = 'builtin://plan-debate'
  AND group_name = 'implementer';

-- +goose StatementEnd
