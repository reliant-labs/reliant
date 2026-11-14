-- forge:backward-compatible — a defaulted column the previous release never reads; its inserts take the default ''.

-- Migration: llm_call_usage_account_id
--
-- Which provider account served a model call (org-provider-accounts.md §3.7):
-- provider_accounts.id, or '' when the call was not routed to an account (a
-- local model, a provider with no accounts) or was written by a release that
-- does not record it. An opaque id, not content: TestLLMUsageTablesHoldNoContent
-- and the privacy page list it. No foreign key: usage history outlives a
-- removed account, as messages.provider_account_id does.
--
-- ADD COLUMN with a constant default is a catalog change, not a rewrite.
--
-- Post-deploy verification (prod): the column exists, and calls routed to an
-- account record it within minutes of the deploy (a non-zero count):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify llm_call_usage.account_id" \
--     "SELECT count(*) FROM llm_call_usage WHERE account_id <> '' AND created_at > now() - interval '1 hour'"
--
-- Rollback: none needed. The previous release's inserts omit the column and
-- take the default ''.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage ADD COLUMN account_id text NOT NULL DEFAULT '';
