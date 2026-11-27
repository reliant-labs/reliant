-- forge:backward-compatible — validates the org_id CHECK on existing rows; every existing row has org_id NULL, which satisfies it, and nothing is written.

-- Migration: attachments_org_id_validate
-- Created: 2026-11-27T02:15:05Z
--
-- 20261127161000_attachments_org_id added attachments_org_id_not_blank NOT
-- VALID. Validate it here, in its own transaction: VALIDATE CONSTRAINT holds
-- SHARE UPDATE EXCLUSIVE, which blocks neither reads nor writes while it
-- scans.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed): the constraint is installed and validated (true):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify attachments org_id constraint validated" \
--     "SELECT convalidated FROM pg_constraint WHERE conname = 'attachments_org_id_not_blank'"
--
-- Rollback: none needed. The column is nullable and the previous release
-- never names it.
SET LOCAL lock_timeout = '5s';
ALTER TABLE attachments VALIDATE CONSTRAINT attachments_org_id_not_blank;
