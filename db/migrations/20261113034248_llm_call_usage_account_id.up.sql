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
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage ADD COLUMN account_id text NOT NULL DEFAULT '';
