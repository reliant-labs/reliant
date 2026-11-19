-- forge:backward-compatible — validates the app_name CHECK on existing rows; every existing row has app_name NULL, which satisfies it, and nothing is written.

-- Migration: chat_app_name_validate
-- Created: 2026-11-19T17:47:22Z
--
-- 20261119174650_chat_app_name added chats_app_name_valid NOT VALID. Validate
-- it here, in its own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE
-- EXCLUSIVE, which does not block reads or writes while it scans. lock_timeout
-- bounds the wait.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed): the CHECK is installed and validated (true):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify chats_app_name_valid validated" \
--     "SELECT convalidated FROM pg_constraint WHERE conname = 'chats_app_name_valid'"
-- A failed VALIDATE fails this migration and the deploy, and leaves the NOT
-- VALID constraint enforcing new rows.
--
-- Rollback: none needed. The column is nullable and the previous release
-- never names it, so rolling the release back leaves it in place.
SET LOCAL lock_timeout = '5s';
ALTER TABLE chats VALIDATE CONSTRAINT chats_app_name_valid;
