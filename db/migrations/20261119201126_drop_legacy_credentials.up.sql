-- forge:breaks-rollback drops the legacy credential tables, legacy_credential_copy and provider_accounts.legacy_source: the previous release names legacy_source in its provider_accounts inserts, credential rewrites and full-row reads, deletes from the *_auth_tokens tables on account purge, and reads legacy_credential_copy at boot, so it fails on those paths against this schema until it is rolled forward again.

-- Migration: drop_legacy_credentials
-- Created: 2026-11-19T20:11:26Z
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

-- Refuse, aborting the whole migration, on a database that holds legacy credentials the copy
-- never saw: one that skipped every release that copies (v1.8.3 onwards) and went
-- straight to this one. Dropping them there would sign every such user out of their provider for
-- good. A database with no legacy rows (a fresh install, a test template) has nothing to lose and
-- passes without a marker.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM legacy_credential_copy)
       AND (EXISTS (SELECT 1 FROM claude_auth_tokens)
            OR EXISTS (SELECT 1 FROM codex_auth_tokens)
            OR EXISTS (SELECT 1 FROM copilot_auth_tokens)
            OR EXISTS (SELECT 1 FROM antigravity_auth_tokens)
            OR EXISTS (SELECT 1 FROM api_keys WHERE provider NOT LIKE 'reliant-automation:%'))
    THEN
        RAISE EXCEPTION 'drop_legacy_credentials: the legacy credential tables still hold credentials and legacy_credential_copy has no completion marker, so they were never copied into provider_accounts'
            USING HINT = 'Boot a release that copies them (one that still has db.MigrateLegacyCredentials, before this migration) against this database once, then upgrade again.';
    END IF;
END $$;

DELETE FROM api_keys WHERE provider NOT LIKE 'reliant-automation:%';

DROP TABLE claude_auth_tokens;
DROP TABLE codex_auth_tokens;
DROP TABLE copilot_auth_tokens;
DROP TABLE antigravity_auth_tokens;
DROP TABLE legacy_credential_copy;

ALTER TABLE provider_accounts DROP COLUMN legacy_source;
