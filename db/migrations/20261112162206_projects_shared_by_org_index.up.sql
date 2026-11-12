-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning, and the index covers only rows (visibility = 'org') that release never writes.

-- Migration: projects_shared_by_org_index
-- Created: 2026-11-12T16:22:06Z
--
-- ListSharedProjects reads an org's shared projects, most recently active
-- first (control-plane docs/design/org-sharing.md §9), as a partial index for
-- the same reason as chats_shared_by_org.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS projects_shared_by_org ON public.projects (org_id, last_active DESC) WHERE visibility = 'org';
