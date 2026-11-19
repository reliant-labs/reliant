-- forge:backward-compatible — a new partial unique index whose key (user_id, remote_url, org) is finer than the existing projects_user_remote_url_uniq (user_id, remote_url): every row the previous release can write satisfies both, and no statement of it names the index.

-- Migration: projects_org_user_remote_url_key
-- Created: 2026-11-19T16:26:32Z
--
-- A repository is one project per (org, user), not per user: the same repo
-- may be a project in two organizations (owner decision, 2026-11-17). The
-- expand half: the old (user_id, remote_url) index stays in this release and
-- a later one drops it, as 20261119162633_projects_org_user_path_key says.
--
-- A row with no org is keyed as in 20261119162633_projects_org_user_path_key:
-- COALESCE(org_id, '') is one "no org yet" bucket per user.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that IF
-- NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY
-- projects_org_user_remote_url_key and re-run.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS projects_org_user_remote_url_key
    ON public.projects (user_id, remote_url, (COALESCE(org_id, '')))
    WHERE remote_url IS NOT NULL;
