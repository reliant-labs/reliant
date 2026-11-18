-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: chats_project_index
-- Created: 2026-11-13T01:10:38Z
--
-- chats.project_id is ON DELETE CASCADE from projects, and DeleteUserProject
-- (internal/store/project_delete.go) reads a project's chats a batch at a
-- time. idx_chats_user_project leads with user_id, so it answers neither: a
-- project's chats are every member's, and the foreign key's lookup names no
-- user. Both were a full scan of chats.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY idx_chats_project
-- and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chats_project ON public.chats (project_id);
