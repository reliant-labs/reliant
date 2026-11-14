-- forge:backward-compatible — validates existing rows against the CHECKs 20261113034239 added NOT VALID; every existing row is a person's with the new columns NULL, so all pass, and nothing is written.

-- Migration: provider_accounts_org_owner_validate
-- Created: 2026-11-13T01:49:42Z
--
-- Validate the CHECKs 20261113034239_provider_accounts_org_owner added NOT
-- VALID, in their own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE
-- EXCLUSIVE, which does not block account reads or writes while it scans.
-- lock_timeout bounds the wait.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed):
--
--   1. All seven CHECKs are installed and validated (true, 7):
--        reliant forge db query prod --database reliant --confirm-env prod \
--          --reason "verify provider_accounts owner checks validated" \
--          "SELECT bool_and(convalidated), count(*) FROM pg_constraint WHERE conrelid = 'provider_accounts'::regclass AND conname IN ('provider_accounts_owner_kind_check', 'provider_accounts_org_owner_key', 'provider_accounts_user_owner_key', 'provider_accounts_org_api_key_only', 'provider_accounts_org_access_check', 'provider_accounts_attribution_check', 'provider_accounts_one_managed')"
--   2. Every existing row is a person's, untouched (0):
--        ... "SELECT count(*) FROM provider_accounts WHERE owner_kind <> 'user' OR org_id IS NOT NULL OR attributed_org_id IS NOT NULL"
--   A failed VALIDATE fails this migration and the deploy, and leaves the NOT
--   VALID constraints enforcing new rows; the release then retries it.
--
-- Rollback: none needed. The columns are defaulted or nullable and the
-- previous release never names them; every row it writes satisfies every
-- CHECK (owner_kind defaults to 'user', its managed identity is 'managed'), so
-- rolling the release back leaves the schema in place and the old binary keeps
-- working. Removing any of it is a later forward migration.
SET LOCAL lock_timeout = '5s';
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_owner_kind_check;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_org_owner_key;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_user_owner_key;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_org_api_key_only;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_org_access_check;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_attribution_check;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_one_managed;
