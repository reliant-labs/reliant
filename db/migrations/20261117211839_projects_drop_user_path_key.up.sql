-- forge:backward-compatible — drops UNIQUE (user_id, path), which no statement of the previous release names (no ON CONFLICT target, no constraint-name match); it decides "a project already exists at this path" by its own (user_id, path) lookup first, so on this schema it still refuses the same path twice for one user, and it never reads two rows where it expects one without a LIMIT.

-- Migration: projects_drop_user_path_key
-- Created: 2026-11-17T21:18:39Z
--
-- The contract half of 20261117211805_projects_org_user_path_key: a project
-- is unique per (org, user, path) now, so the same folder can be a project in
-- two organizations. The old key would refuse that, so it cannot stay beside
-- the new one.
--
-- Why it is safe to drop in the release that adds the new key: the previous
-- release relies on the old constraint only as a backstop. It has no ON
-- CONFLICT on it, and the one place that matches its violation
-- (CreateProject) matches any "duplicate key" text, which the new index
-- raises too. Every lookup it makes by (user_id, path) is LIMIT 1, so a
-- second row in another org is never a scan error, and its own create checks
-- refuse a second project at a path the user already has (org-blind), as
-- they did before.
--
-- DROP CONSTRAINT takes an ACCESS EXCLUSIVE lock for the catalog change only
-- (the index behind it is dropped with it); lock_timeout bounds the wait
-- behind a long transaction: the migration fails after 5s and the next
-- deploy retries it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_user_id_path_key;
