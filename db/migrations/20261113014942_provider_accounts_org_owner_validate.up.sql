-- forge:backward-compatible — validates existing rows against the CHECKs 20261113014921 added NOT VALID; every existing row is a person's with the new columns NULL, so all pass, and nothing is written.

-- Migration: provider_accounts_org_owner_validate
-- Created: 2026-11-13T01:49:42Z
--
-- Validate the CHECKs 20261113014921_provider_accounts_org_owner added NOT
-- VALID, in their own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE
-- EXCLUSIVE, which does not block account reads or writes while it scans.
-- lock_timeout bounds the wait.
SET LOCAL lock_timeout = '5s';
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_owner_kind_check;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_org_owner_key;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_user_owner_key;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_org_api_key_only;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_org_access_check;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_attribution_check;
ALTER TABLE provider_accounts VALIDATE CONSTRAINT provider_accounts_one_managed;
