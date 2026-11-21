-- forge:breaks-rollback drops api_keys.api_key: the previous release writes it (always '') in every automation-token upsert and its boot backfill selects it, so it fails to save an automation token, and to boot, against this schema until it is rolled forward again. It needs the owner's migration:breaks-rollback-approved label.

-- Migration: drop_plaintext_api_key
-- Created: 2026-11-21T03:43:20Z
--
-- api_keys.api_key is the plaintext column sealing replaced. Every reader has opened only
-- api_key_sealed since the sealing contract, writers have written api_key as '', and every boot
-- sealed any row still holding plaintext (db.sealAPIKeys) and refused to start while one
-- remained. That backfill is gone, so this drops the column and validates the
-- api_keys_sealed_not_null check the boot used to validate.
-- forge:allow-destructive

-- Refuse, aborting the whole migration, while any row still holds a plaintext key that was never
-- sealed: dropping the column would lose it. Only a database that skipped every release that
-- seals at boot can have one.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM api_keys WHERE api_key_sealed IS NULL AND api_key <> '') THEN
        RAISE EXCEPTION 'drop_plaintext_api_key: api_keys has rows with a plaintext api_key and no api_key_sealed; dropping the column would lose them'
            USING HINT = 'Boot a release that seals api_keys at startup (one that still has db.sealAPIKeys, before this migration) against this database once, then upgrade again.';
    END IF;
END $$;

-- A row with neither value holds no credential (every reader refuses it already), and would fail
-- the validation below.
DELETE FROM api_keys WHERE api_key_sealed IS NULL;

ALTER TABLE api_keys VALIDATE CONSTRAINT api_keys_sealed_not_null;

ALTER TABLE api_keys DROP COLUMN api_key;
