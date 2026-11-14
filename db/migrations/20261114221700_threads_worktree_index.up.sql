-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning.

-- Migration: threads_worktree_index
-- Created: 2026-11-13T01:09:19Z
--
-- threads.worktree_id is ON DELETE SET NULL against worktrees, so every
-- worktree delete looks up the threads that ran in it. With no index that
-- lookup is a full scan of threads PER WORKTREE deleted.
-- That is what made DeleteProject on a project with 1.4M messages run for 17
-- minutes holding the project row, with every write for that user queued
-- behind it (prod, 2026-11-13).
--
-- Partial on IS NOT NULL: the referential-integrity lookup is an equality on
-- the column, which implies it, so NULL rows need no entry.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY idx_threads_worktree
-- and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_threads_worktree ON public.threads (worktree_id) WHERE worktree_id IS NOT NULL;
