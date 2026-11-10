-- forge:backward-compatible — validates the org_id CHECK on existing rows; every existing row has org_id NULL, which satisfies it, and nothing is written.

-- Migration: user_updates_org_id_validate
-- Created: 2026-11-10T11:56:38Z
--
-- 20261110115604_user_updates_org_id added user_updates_org_id_not_blank NOT
-- VALID. Validate it here, in its own transaction: VALIDATE CONSTRAINT holds
-- SHARE UPDATE EXCLUSIVE, which does not block reads or writes while it scans.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed):
--
--   1. The CHECK is installed and validated (true):
--        reliant forge db query prod --database reliant --confirm-env prod \
--          --reason "verify user_updates org check validated" \
--          "SELECT convalidated FROM pg_constraint WHERE conname = 'user_updates_org_id_not_blank'"
--   2. Updates about an org's chat are stamped: none written since the deploy
--      about a chat that has an org lacks one (0):
--        "SELECT count(*) FROM user_updates u JOIN chats c ON c.id = u.chat_id
--          WHERE u.created_at > now() - interval '1 hour' AND c.org_id IS NOT NULL AND u.org_id IS NULL"
--
-- Rollback: roll back the binary. The column, check and trigger are inert to
-- the previous release, which never names org_id.
SET LOCAL lock_timeout = '5s';

ALTER TABLE user_updates VALIDATE CONSTRAINT user_updates_org_id_not_blank;
