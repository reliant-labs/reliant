-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning.

-- Migration: threads_fork_at_message_index
-- Created: 2026-11-13T01:09:07Z
--
-- threads.fork_at_message_id is ON DELETE RESTRICT against messages, so every
-- message delete looks up the threads forked at it. With no index that lookup
-- is a full scan of threads PER MESSAGE deleted.
-- That is what made DeleteProject on a project with 1.4M messages run for 17
-- minutes holding the project row, with every write for that user queued
-- behind it (prod, 2026-11-13).
--
-- Partial on IS NOT NULL: the referential-integrity lookup is an equality on
-- the column, which implies it, so NULL rows need no entry.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY idx_threads_fork_at_message
-- and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_threads_fork_at_message ON public.threads (fork_at_message_id) WHERE fork_at_message_id IS NOT NULL;
