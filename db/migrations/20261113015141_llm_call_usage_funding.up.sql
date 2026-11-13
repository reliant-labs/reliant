-- forge:backward-compatible — a defaulted column the previous release never reads; its inserts take the default '', which the new CHECK admits.

-- Migration: llm_call_usage_funding
-- Created: 2026-11-13T01:51:41Z
--
-- Who paid for a model call (org-provider-accounts.md §2, §3.7): the funding
-- class of the account that served it, derived from the account row at the
-- call and never stored on the account.
--
--   org_key     an API key the chat's org owns
--   org_seat    the person's sign-in to a workspace the chat's org pays for
--   org_wallet  the managed Reliant key minted in the chat's org
--   personal    the person's own subscription or API key
--   local       a model on the person's own hardware
--   ''          not recorded (rows written before this column, or by a
--               release that does not write it)
--
-- A closed-set label, not content: TestLLMUsageTablesHoldNoContent and the
-- privacy page list it. ADD COLUMN with a constant default is a catalog change,
-- not a rewrite. The CHECK is added NOT VALID, so the lock is held only for
-- the catalog change; 20261113015205_llm_call_usage_funding_validate validates
-- the existing rows (all '') in its own transaction.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage ADD COLUMN funding text NOT NULL DEFAULT '';
ALTER TABLE llm_call_usage ADD CONSTRAINT llm_call_usage_funding_check
    CHECK (funding IN ('', 'org_key', 'org_seat', 'org_wallet', 'personal', 'local')) NOT VALID;
