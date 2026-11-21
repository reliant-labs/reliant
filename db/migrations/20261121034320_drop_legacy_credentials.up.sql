-- forge:breaks-rollback drops the legacy credential tables, legacy_credential_copy and provider_accounts.legacy_source: the previous release names legacy_source in its provider_accounts inserts, credential rewrites and full-row reads, deletes from the *_auth_tokens tables on account purge, and reads legacy_credential_copy at boot, so it fails on those paths against this schema until it is rolled forward again. It irreversibly deletes the legacy credential rows: it needs the owner's migration:breaks-rollback-approved label.

-- Migration: drop_legacy_credentials
-- Created: 2026-11-21T03:43:20Z
--
-- The contract half of 20261019033847_provider_accounts and
-- 20261019120616_legacy_credential_copy_done. Since those, every LLM credential lives in
-- provider_accounts; claude_auth_tokens, codex_auth_tokens, copilot_auth_tokens,
-- antigravity_auth_tokens and the LLM rows of api_keys were read only by the one-time boot copy
-- (db.MigrateLegacyCredentials), which wrote the legacy_credential_copy marker when it finished.
-- That copy is gone, so this drops what only it used: the four tables, the marker table,
-- provider_accounts.legacy_source (which told the copy an account it made from a native one), and
-- the LLM rows of api_keys. api_keys itself stays: it holds the automation tokens, the rows whose
-- provider is 'reliant-automation:<daemon id>' (core.AutomationProviderPrefix).
-- forge:allow-destructive

-- Refuse, aborting the whole migration, while the legacy tables may still hold a credential that
-- provider_accounts does not. That is the case when legacy rows exist and either:
--   * legacy_credential_copy has no marker: the database skipped every release that copies
--     (v1.8.3 onwards) and went straight to this one; or
--   * the marker's failed count is above zero: the copy wrote its marker anyway, after counting
--     rows it could not open, that failed validation, or whose write failed. Those rows were
--     never copied, so the marker alone proves nothing about them.
-- Dropping the tables there would sign those users out of their provider for good. An operator
-- who has reconciled the failed rows (re-connected those users, or decided they are unopenable)
-- records that with UPDATE legacy_credential_copy SET failed = 0, then upgrades again. A
-- database with no legacy rows (a fresh install, a test template) has nothing to lose and
-- passes either way.
DO $$
DECLARE
    copy_failed integer;
BEGIN
    IF EXISTS (SELECT 1 FROM claude_auth_tokens)
       OR EXISTS (SELECT 1 FROM codex_auth_tokens)
       OR EXISTS (SELECT 1 FROM copilot_auth_tokens)
       OR EXISTS (SELECT 1 FROM antigravity_auth_tokens)
       OR EXISTS (SELECT 1 FROM api_keys WHERE provider NOT LIKE 'reliant-automation:%')
    THEN
        SELECT failed INTO copy_failed FROM legacy_credential_copy WHERE id = 1;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'drop_legacy_credentials: the legacy credential tables still hold credentials and legacy_credential_copy has no completion marker, so they were never copied into provider_accounts'
                USING HINT = 'Boot a release that copies them (one that still has db.MigrateLegacyCredentials, before this migration) against this database once, then upgrade again.';
        END IF;
        IF copy_failed > 0 THEN
            RAISE EXCEPTION 'drop_legacy_credentials: the legacy credential copy recorded % row(s) it failed to copy into provider_accounts; dropping the legacy tables would lose them', copy_failed
                USING HINT = 'Reconcile those rows (the copy logged each at WARN as "legacy credential skipped"), then record it with UPDATE legacy_credential_copy SET failed = 0 and upgrade again.';
        END IF;
    END IF;
END $$;

DELETE FROM api_keys WHERE provider NOT LIKE 'reliant-automation:%';

DROP TABLE claude_auth_tokens;
DROP TABLE codex_auth_tokens;
DROP TABLE copilot_auth_tokens;
DROP TABLE antigravity_auth_tokens;
DROP TABLE legacy_credential_copy;

ALTER TABLE provider_accounts DROP COLUMN legacy_source;
