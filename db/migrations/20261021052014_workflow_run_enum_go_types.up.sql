-- forge:backward-compatible — catalog comments only; no table or column definition changes, and the previous release never reads comments.

-- CHECK-constrained text columns that hold an internal/db/core string enum
-- project to that type on the generated entity (internal/store), so the
-- workflow/run lane maps the field without a cast.
COMMENT ON COLUMN run_events.outcome IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.RunEventOutcome';
COMMENT ON COLUMN workflow_drafts.status IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.WorkflowDraftStatus';
