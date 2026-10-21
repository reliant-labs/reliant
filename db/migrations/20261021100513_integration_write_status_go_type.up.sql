-- forge:backward-compatible — a catalog comment only; no table or column definition changes, and the previous release never reads comments.

-- integration_writes.status holds core.IntegrationWriteStatus (intent, done,
-- failed; the CHECK constraint is the backstop). It projects to that type on
-- the generated entity (P5b lane L5), so the ported ledger queries name the
-- status they filter on instead of a bare string.
COMMENT ON COLUMN integration_writes.status IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.IntegrationWriteStatus';
