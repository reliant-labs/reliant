-- forge:backward-compatible — catalog comments only; no table or column definition changes, and the previous release never reads comments.

-- Text columns of the trigger aggregate (P5b lane L4) whose values are a core
-- enum project to that enum on the generated entity, so a ported query names
-- its filter (core.TriggerEventPending, core.TriggerKindIntegration) instead
-- of a bare string. Each column's CHECK constraint is the value set; the core
-- type is its name in Go.
COMMENT ON COLUMN triggers.kind IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.TriggerKind';
COMMENT ON COLUMN triggers.acting_identity IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.ActingIdentity';
COMMENT ON COLUMN trigger_events.kind IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.TriggerEventKind';
COMMENT ON COLUMN trigger_events.outcome IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.TriggerEventOutcome';
