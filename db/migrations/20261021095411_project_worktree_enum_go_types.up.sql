-- forge:backward-compatible — catalog comments only; no table or column changes, and the previous release never reads comments.

-- The enum columns of the L3 project/worktree tables project to the type the
-- domain already uses for them, so the ported queries in internal/store
-- filter and map on named values rather than raw ints and strings.
COMMENT ON COLUMN project_daemons.install_state IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.ProjectInstallState';
COMMENT ON COLUMN worktrees.status IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorktreeStatus';
COMMENT ON COLUMN visibility_overrides.item_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.HiddenItemType';
