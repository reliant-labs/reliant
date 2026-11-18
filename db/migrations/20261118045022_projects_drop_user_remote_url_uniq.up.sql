-- forge:backward-compatible — drops projects_user_remote_url_uniq. The rollback target is a release that already resolves every project by org (reliant#2861) and was tested against this exact schema (db.RelaxProjectKeysForTest), and no statement of it names this index.

-- Migration: projects_drop_user_remote_url_uniq
-- Created: 2026-11-18T04:50:22Z
--
-- Release 2 of the org keys: the contract half of
-- 20261117211828_projects_org_user_remote_url_key. A repository is one
-- project per (org, user) from here, so the same repo can be a project in two
-- organizations. Ships under the same condition as
-- 20261118045005_projects_drop_user_path_key.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.projects_user_remote_url_uniq;
