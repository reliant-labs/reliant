-- forge:backward-compatible — catalog comments only; no table or column changes, and the previous release never reads comments.

-- The enum columns of the L3 daemon and background-process tables project to
-- the type the domain already uses for them, so the ported queries in
-- internal/store map the field without a cast.
COMMENT ON COLUMN background_processes.status IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.BackgroundProcessStatus';
COMMENT ON COLUMN background_processes.source_type IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.BackgroundProcessSourceType';
COMMENT ON COLUMN daemon_attachment.source IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.DaemonAttachmentSource';
