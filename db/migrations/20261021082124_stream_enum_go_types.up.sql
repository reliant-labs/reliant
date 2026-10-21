-- forge:backward-compatible — catalog comments only; no table or column changes, and the previous release never reads comments.

-- Integer columns of the L1a stream tables that hold an enum project to the
-- type internal/db/core already uses for them, so the ported queries in
-- internal/store map the field without a cast.
COMMENT ON COLUMN tool_calls.status IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.ToolCallStatus';
COMMENT ON COLUMN user_updates.update_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.UserUpdateType';
COMMENT ON COLUMN user_updates.entity_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.EntityType';
