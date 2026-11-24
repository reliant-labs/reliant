-- forge:backward-compatible — validates existing rows against the checks 20261124162017_llm_call_usage_run_scope added NOT VALID; every existing row holds NULL in the new columns, which they admit, and nothing is written.

-- Migration: llm_call_usage_run_scope_validate
-- Created: 2026-11-24T16:20:18Z
--
-- Validate the CHECKs 20261124162017_llm_call_usage_run_scope added NOT VALID,
-- in their own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE,
-- which does not block usage writes while it scans. lock_timeout bounds the wait.
--
-- Post-deploy verification (prod): the three CHECKs are validated, and calls
-- made since the deploy record their run (a non-zero count within minutes):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify llm_call_usage run scope" \
--     "SELECT (SELECT bool_and(convalidated) FROM pg_constraint WHERE conname IN ('llm_call_usage_cost_basis_check', 'llm_call_usage_cost_matches_basis', 'llm_call_usage_run_scope_nonnegative')), (SELECT count(*) FROM llm_call_usage WHERE workflow_id IS NOT NULL AND created_at > now() - interval '1 hour')"
--
-- Rollback: none needed. The previous release's inserts omit the new columns
-- and leave them NULL, which every CHECK admits.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_cost_basis_check;
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_cost_matches_basis;
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_run_scope_nonnegative;
