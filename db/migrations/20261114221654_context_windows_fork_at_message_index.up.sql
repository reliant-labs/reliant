-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning.

-- Migration: context_windows_fork_at_message_index
-- Created: 2026-11-13T01:09:12Z
--
-- context_windows.fork_at_message_id is ON DELETE RESTRICT against messages,
-- so every message delete looks up the context windows forked at it. With no
-- index that lookup is a full scan of context_windows PER MESSAGE deleted.
-- That is what made DeleteProject on a project with 1.4M messages run for 17
-- minutes holding the project row, with every write for that user queued
-- behind it (prod, 2026-11-13).
--
-- Partial on IS NOT NULL: the referential-integrity lookup is an equality on
-- the column, which implies it, so NULL rows need no entry.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY idx_context_windows_fork_at_message
-- and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_context_windows_fork_at_message ON public.context_windows (fork_at_message_id) WHERE fork_at_message_id IS NOT NULL;
