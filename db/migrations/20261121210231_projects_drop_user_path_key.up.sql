-- forge:backward-compatible — drops UNIQUE (user_id, path). The rollback target is a release that already resolves every project by org (reliant#2861) and was tested against this exact schema (db.RelaxProjectKeysForTest), and no statement of it names this constraint (no ON CONFLICT target; its create matches any "duplicate key", which projects_org_user_path_key raises).

-- Migration: projects_drop_user_path_key
-- Created: 2026-11-21T21:02:31Z
--
-- Release 2 of the org keys: the contract half of
-- 20261118111647_projects_org_user_path_key. A project is unique per (org,
-- user, path) from here, so the same folder can be a project in two
-- organizations (owner decision, 2026-11-17).
--
-- SHIP ONLY AFTER: a release containing reliant#2861 (release 1: org-aware
-- readers everywhere) is the rollback target of the release this ships in.
-- The release before release 1 resolves projects by (user_id, path) with no
-- org, so on this schema it could pick another org's project.
--
-- DROP CONSTRAINT takes an ACCESS EXCLUSIVE lock for the catalog change only
-- (the index behind it is dropped with it); lock_timeout bounds the wait
-- behind a long transaction: the migration fails after 5s and the next
-- deploy retries it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_user_id_path_key;
