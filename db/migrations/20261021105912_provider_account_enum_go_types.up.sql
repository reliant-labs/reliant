-- forge:backward-compatible — catalog comments only; no table or column definition changes, and the previous release never reads comments.

-- provider_accounts.auth_kind holds a core.AuthKind (its CHECK lists exactly
-- the four constants), so the generated entity carries that type and the
-- accounts aggregate (P5b lane L6) maps it without a conversion.
COMMENT ON COLUMN provider_accounts.auth_kind IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.AuthKind';
