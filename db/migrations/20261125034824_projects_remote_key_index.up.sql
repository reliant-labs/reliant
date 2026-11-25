-- forge:backward-compatible — a new non-unique partial index; it refuses no write, and no statement of the previous release names it.

-- Migration: projects_remote_key_index
-- Created: 2026-11-25T03:48:24Z
--
-- Serves the lookup that makes a machine's folder join the project it is a
-- checkout of (GetProjectByRemoteKeyAndUserInScope): the user's projects of
-- one repository. Keyed the way project uniqueness is (user, key, org; see
-- 20261119162633_projects_org_user_path_key), but NOT unique: existing
-- duplicates of one repository would fail the build, and merging them is the
-- user's decision (20261125034821_projects_remote_key).
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that IF
-- NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY
-- projects_user_remote_key_idx and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS projects_user_remote_key_idx
    ON public.projects (user_id, remote_key, (COALESCE(org_id, '')))
    WHERE remote_key IS NOT NULL;
