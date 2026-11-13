-- forge:backward-compatible — additive partial index over rows the previous release never writes; it changes no result.

-- Migration: provider_accounts_org_index
-- Created: 2026-11-13T01:49:52Z
--
-- The org-scoped account loader (org-provider-accounts.md §4.1) reads an org's
-- accounts by (org_id, provider, priority) WHERE owner_kind = 'org'. Partial:
-- person rows, nearly all of the table, are not in it.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
--
-- Rollback: none needed. An extra index changes no result; the previous
-- release ignores it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS provider_accounts_org ON public.provider_accounts USING btree (org_id, provider, priority) WHERE owner_kind = 'org';
