-- +goose Up

-- Credential vault, contract step (research/CONNECTIONS_VAULT.md §2.5).
-- api_keys.api_key_sealed is now the only source of truth: readers are
-- sealed-only and writers store '' in the legacy api_key column.
--
-- Why a NOT VALID check and not SET NOT NULL: this migration runs before the
-- app's boot backfill (db.BootVault), so a legacy row with a NULL sealed value
-- may still exist, and a plain SET NOT NULL would fail the whole migration and
-- keep every process from starting. NOT VALID enforces the rule on every new or
-- updated row immediately without scanning existing ones; BootVault runs
-- VALIDATE CONSTRAINT once the backfill has sealed every row (and refuses to
-- start if any row stays unsealed).
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'api_keys_sealed_not_null' AND conrelid = 'api_keys'::regclass
  ) THEN
    ALTER TABLE api_keys
      ADD CONSTRAINT api_keys_sealed_not_null CHECK (api_key_sealed IS NOT NULL) NOT VALID;
  END IF;
END
$$;
-- +goose StatementEnd

-- Blank the plaintext wherever a sealed copy already exists. Rows without one
-- are untouched so the backfill can still seal them.
UPDATE api_keys SET api_key = '' WHERE api_key_sealed IS NOT NULL AND api_key <> '';

-- The api_key column stays for now: dropping it is a later contract step once
-- no release reads or writes it.
