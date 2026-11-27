-- forge:backward-compatible — validates the org_id CHECKs and project_id foreign keys on existing rows; every existing row has both NULL, which satisfies them, and nothing is written.

-- Migration: pr_records_tenancy_validate
-- Created: 2026-11-26T10:59:45Z
--
-- 20261126105940_pr_records_tenancy added ten constraints NOT VALID. Validate
-- them here, in their own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE
-- EXCLUSIVE (and, for a foreign key, ROW SHARE on projects), which blocks
-- neither reads nor writes while it scans.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed): all ten are installed and validated (true, 10):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pr_records tenancy constraints validated" \
--     "SELECT bool_and(convalidated), count(*) FROM pg_constraint WHERE conrelid::regclass::text LIKE 'pr_record%' AND (conname LIKE '%_org_id_not_blank' OR conname LIKE '%_project_id_fkey')"
--
-- Rollback: none needed. The columns are nullable and the previous release
-- never names them.
SET LOCAL lock_timeout = '5s';
ALTER TABLE pr_records VALIDATE CONSTRAINT pr_records_org_id_not_blank;
ALTER TABLE pr_records VALIDATE CONSTRAINT pr_records_project_id_fkey;
ALTER TABLE pr_record_heads VALIDATE CONSTRAINT pr_record_heads_org_id_not_blank;
ALTER TABLE pr_record_heads VALIDATE CONSTRAINT pr_record_heads_project_id_fkey;
ALTER TABLE pr_record_reviews VALIDATE CONSTRAINT pr_record_reviews_org_id_not_blank;
ALTER TABLE pr_record_reviews VALIDATE CONSTRAINT pr_record_reviews_project_id_fkey;
ALTER TABLE pr_record_checks VALIDATE CONSTRAINT pr_record_checks_org_id_not_blank;
ALTER TABLE pr_record_checks VALIDATE CONSTRAINT pr_record_checks_project_id_fkey;
ALTER TABLE pr_record_deliveries VALIDATE CONSTRAINT pr_record_deliveries_org_id_not_blank;
ALTER TABLE pr_record_deliveries VALIDATE CONSTRAINT pr_record_deliveries_project_id_fkey;
