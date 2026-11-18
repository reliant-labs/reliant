-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: projects_delete_after_index
-- Created: 2026-11-18T04:06:26Z
--
-- The project purge worker asks, every pass, for the projects whose
-- scheduled deletion is due (delete_after <= now()), and "Recently deleted"
-- lists a user's scheduled projects. Both read only the few rows with
-- delete_after set, so the index is partial and holds only those.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY
-- idx_projects_delete_after and re-run.
--
-- Rollback: none needed. An extra index changes no result; the previous
-- release ignores it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_projects_delete_after ON public.projects (delete_after) WHERE delete_after IS NOT NULL;
