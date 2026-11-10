-- forge:backward-compatible — validates the org_id CHECKs on existing rows; every existing row has org_id NULL, which satisfies them, and nothing is written.

-- Migration: org_id_projects_chats_validate
-- Created: 2026-11-10T02:12:53Z
--
-- 20261110021244_org_id_projects_chats added projects_org_id_not_blank and
-- chats_org_id_not_blank NOT VALID. Validate them here, in their own
-- transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which does not
-- block reads or writes while it scans. lock_timeout bounds the wait.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed):
--
--   1. Both CHECKs are installed and validated (true, 2):
--        reliant forge db query prod --database reliant --confirm-env prod \
--          --reason "verify org_id checks validated" \
--          "SELECT bool_and(convalidated), count(*) FROM pg_constraint WHERE conname IN ('projects_org_id_not_blank', 'chats_org_id_not_blank')"
--      A failed VALIDATE fails this migration and the deploy, and leaves the
--      NOT VALID constraints enforcing new rows.
--
-- Rollback: none needed. The columns are nullable and the previous release
-- never names them, so rolling the release back leaves them in place and the
-- old binary keeps working. Dropping them is a later forward migration, once
-- no release reads them.
SET LOCAL lock_timeout = '5s';
ALTER TABLE projects VALIDATE CONSTRAINT projects_org_id_not_blank;
ALTER TABLE chats VALIDATE CONSTRAINT chats_org_id_not_blank;
