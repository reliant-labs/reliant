-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning.

-- Migration: provider_accounts_org_needs_attention_index
-- Created: 2026-11-13T10:45:06Z
--
-- The inbox lists an org's rejected API keys to its owners and admins
-- (org-provider-accounts.md §4.7): org-owned accounts that need attention,
-- by org. The rows are few (a key the provider refused, until it is rotated),
-- so a partial index keeps every inbox read from scanning provider_accounts.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS provider_accounts_org_needs_attention ON public.provider_accounts (org_id) WHERE owner_kind = 'org' AND needs_reauth_at IS NOT NULL;
