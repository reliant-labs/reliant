-- forge:backward-compatible — validates the existing rows against the CHECKs 20261114055948 added NOT VALID; every existing row is a workspace grant with a daemon and a path_root, so each holds, and nothing is written.
-- Migration: connector_grant_kind_validate
-- Created: 2026-11-14T06:00:01Z
--
-- The schema is the 166 migration(s) in this directory, applied in order (newest: 20261114055948_connector_grant_kind.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:


-- 20261114055948_connector_grant_kind added connector_grants' kind, forge-org,
-- project_names, daemon-by-kind and path_root CHECKs NOT VALID, so its ACCESS
-- EXCLUSIVE lock covered only catalog changes. Validate them here, in their own
-- transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which does not
-- block connector reads or writes while it scans. lock_timeout bounds the wait.
--
-- Every row that existed before 20261114055948 defaulted to kind 'workspace',
-- org_id '' and project_names '[]', and had a daemon_id and a non-empty
-- path_root (the NOT NULL and the CHECK it replaced), so each constraint holds.
-- A failed VALIDATE fails this migration and the deploy, and leaves the NOT
-- VALID constraints enforcing new and updated rows.
--
-- Post-deploy verification (prod):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify connector_grants CHECKs validated" \
--     "SELECT conname, convalidated FROM pg_constraint WHERE conrelid = 'public.connector_grants'::regclass AND contype = 'c' ORDER BY conname"
--   (every row convalidated = true)
--
-- Rollback: none needed. Validation changes no data and no definition; the
-- previous release ignores these constraints exactly as it does when they are
-- NOT VALID.
SET LOCAL lock_timeout = '5s';
ALTER TABLE connector_grants VALIDATE CONSTRAINT connector_grants_kind_valid;
ALTER TABLE connector_grants VALIDATE CONSTRAINT connector_grants_forge_org;
ALTER TABLE connector_grants VALIDATE CONSTRAINT connector_grants_project_names_array;
ALTER TABLE connector_grants VALIDATE CONSTRAINT connector_grants_daemon_by_kind;
ALTER TABLE connector_grants VALIDATE CONSTRAINT connector_grants_path_root_not_empty;
