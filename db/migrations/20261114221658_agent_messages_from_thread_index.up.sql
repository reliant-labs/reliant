-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: agent_messages_from_thread_index
-- Created: 2026-11-13T01:09:17Z
--
-- agent_messages.from_thread_id is ON DELETE CASCADE from threads, so every
-- thread delete looks up the agent messages it sent. With no index that lookup
-- is a full scan of agent_messages PER THREAD deleted.
-- That is what made DeleteProject on a project with 1.4M messages run for 17
-- minutes holding the project row, with every write for that user queued
-- behind it (prod, 2026-11-13).
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY idx_agent_messages_from_thread
-- and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_messages_from_thread ON public.agent_messages (from_thread_id);
