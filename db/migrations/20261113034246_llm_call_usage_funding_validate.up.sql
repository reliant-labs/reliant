-- forge:backward-compatible — validates existing rows against llm_call_usage_funding_check; every existing row holds the default '', which it admits, and nothing is written.

-- Migration: llm_call_usage_funding_validate
-- Created: 2026-11-13T01:52:05Z
--
-- Validate the CHECK 20261113034245_llm_call_usage_funding added NOT VALID, in
-- its own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which
-- does not block usage writes while it scans. lock_timeout bounds the wait.
--
-- Post-deploy verification (prod): the CHECK is validated and nothing has a
-- funding value yet (true, 0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify llm_call_usage funding" \
--     "SELECT (SELECT convalidated FROM pg_constraint WHERE conname = 'llm_call_usage_funding_check'), (SELECT count(*) FROM llm_call_usage WHERE funding <> '')"
--
-- Rollback: none needed. The previous release's inserts omit funding and take
-- the default '', which the CHECK admits.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_funding_check;
