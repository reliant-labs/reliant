-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: llm_call_usage_insights_unfolded_idx
-- Created: 2026-11-25T00:27:44Z
--
-- The rows the insights rollups have not counted yet
-- (20261125002742_insights_rollups): the backfill reads them oldest first.
-- Partial, so once the backfill has drained the existing rows the index holds
-- only what a previous release writes during a rollout.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
--
-- Rollback: none needed. An extra index changes no result; the previous
-- release ignores it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS llm_call_usage_insights_unfolded_idx ON public.llm_call_usage (created_at) WHERE NOT insights_folded;
