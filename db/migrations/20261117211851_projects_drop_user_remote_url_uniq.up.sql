-- forge:backward-compatible — drops projects_user_remote_url_uniq, which no statement of the previous release names (no ON CONFLICT target); it checks a remote is free with its own (user_id, remote_url) lookup before writing one, so on this schema it still refuses one user's repo twice, and every such lookup is LIMIT 1.

-- Migration: projects_drop_user_remote_url_uniq
-- Created: 2026-11-17T21:18:51Z
--
-- The contract half of 20261117211828_projects_org_user_remote_url_key: a
-- repository is one project per (org, user) now, so the same repo can be a
-- project in two organizations, which the old index would refuse.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.projects_user_remote_url_uniq;
