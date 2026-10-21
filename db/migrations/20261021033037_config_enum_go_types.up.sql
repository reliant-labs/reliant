-- forge:backward-compatible — catalog comments only; no table or column definition changes, and the previous release never reads comments.

-- Integer columns of the config aggregate (P5b lane L7) that hold a reliantv1
-- proto enum project to that enum on the generated entity, so a ported query
-- names its filter (APPROVAL_STATUS_PENDING, DEPENDENCY_TYPE_BLOCKS) instead
-- of a bare number. internal/db/core still carries these as int32; the
-- internal/store mappers convert at the boundary.
COMMENT ON COLUMN approvals.status IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ApprovalStatus';
COMMENT ON COLUMN approvals.approval_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ApprovalType';
COMMENT ON COLUMN plans.status IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.PlanStatus';
COMMENT ON COLUMN plans.complexity IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.PlanComplexity';
COMMENT ON COLUMN tasks.status IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.TaskStatus';
COMMENT ON COLUMN task_dependencies.dependency_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.DependencyType';
COMMENT ON COLUMN item_defaults.item_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.HiddenItemType';
