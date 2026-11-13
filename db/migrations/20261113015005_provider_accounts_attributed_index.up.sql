-- forge:backward-compatible — additive partial index over a column the previous release never writes; it changes no result.

-- Migration: provider_accounts_attributed_index
-- Created: 2026-11-13T01:50:05Z
--
-- Seats attributed to an org (org-provider-accounts.md §3.4, §6) are found by
-- attributed_org_id: the workspace matcher, unbinding a workspace, releasing
-- a seat and the org-deletion sweep. Partial: only attributed rows.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
--
-- Rollback: none needed. An extra index changes no result; the previous
-- release ignores it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS provider_accounts_attributed ON public.provider_accounts USING btree (attributed_org_id) WHERE attributed_org_id IS NOT NULL;
