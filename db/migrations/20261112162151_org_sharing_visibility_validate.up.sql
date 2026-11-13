-- forge:backward-compatible — validates the visibility CHECKs on existing rows; every existing row has visibility NULL, which satisfies them, and nothing is written.

-- Migration: org_sharing_visibility_validate
-- Created: 2026-11-12T16:21:51Z
--
-- 20261112162146_org_sharing_visibility added projects_visibility_valid,
-- chats_visibility_valid and workflow_drafts_visibility_valid NOT VALID.
-- Validate them here, in their own transaction: VALIDATE CONSTRAINT holds SHARE
-- UPDATE EXCLUSIVE, which does not block reads or writes while it scans.
-- lock_timeout bounds the wait.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed):
--
--   1. The three CHECKs are installed and validated (true, 3):
--        reliant forge db query prod --database reliant --confirm-env prod \
--          --reason "verify visibility checks validated" \
--          "SELECT bool_and(convalidated), count(*) FROM pg_constraint WHERE conname IN ('projects_visibility_valid', 'chats_visibility_valid', 'workflow_drafts_visibility_valid')"
--      A failed VALIDATE fails this migration and the deploy, and leaves the
--      NOT VALID constraints enforcing new rows.
--
-- Rollback: none needed. The columns are nullable and the previous release
-- never names them, so rolling the release back leaves them in place and the
-- old binary keeps working (every row private to it).
SET LOCAL lock_timeout = '5s';
ALTER TABLE projects VALIDATE CONSTRAINT projects_visibility_valid;
ALTER TABLE chats VALIDATE CONSTRAINT chats_visibility_valid;
ALTER TABLE workflow_drafts VALIDATE CONSTRAINT workflow_drafts_visibility_valid;
