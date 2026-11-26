-- forge:backward-compatible — adds nullable columns and NOT VALID constraints every existing row (all NULL) satisfies; the previous release never names org_id or project_id on these tables and its inserts keep succeeding.

-- Migration: pr_records_tenancy
-- Created: 2026-11-26T10:59:40Z
--
-- PR records belong to an organization and a project once their repository
-- is enrolled in the pipeline (pipeline redesign §4 tenancy; build plan task
-- R2), as every pipeline table does. Nothing is enrolled yet, so nothing is
-- backfilled: the pipeline's projector (R8) stamps both from the enrolled
-- pipeline_repos row when it writes a record of an enrolled repository.
--
--   - org_id: control-plane's organizations.id. An id, no foreign key, as on
--     every other org-scoped table (20261110033439_org_id_workflows_triggers_runs).
--   - project_id: the enrolling project. A foreign key that clears itself
--     when the project goes: a record mirrors the provider's pull request,
--     which outlives any one enrolment, so deleting a project unstamps its
--     records rather than deleting them.
--
-- Every table the projector writes gets both, so a tenant-scoped read or
-- purge of any of them needs no join: pr_record_checks and
-- pr_record_deliveries have no record to join through.
--
-- NULL is every row written before this release, and every row of a
-- repository no project has enrolled. It means what it meant before: no
-- tenant has claimed it.
--
-- The constraints are added NOT VALID, so the ACCESS EXCLUSIVE lock covers
-- only the catalog change; 20261126105945_pr_records_tenancy_validate
-- validates them. lock_timeout bounds the wait behind a long transaction.
--
-- No index on project_id yet: nothing reads by it until R6b's store layer,
-- which adds the indexes its queries need (CONCURRENTLY, each in its own
-- file). Until then a project delete scans these tables once for the SET
-- NULL, as it does every table with an unindexed reference.
SET LOCAL lock_timeout = '5s';

ALTER TABLE pr_records
    ADD COLUMN org_id text,
    ADD COLUMN project_id text,
    ADD CONSTRAINT pr_records_org_id_not_blank CHECK (org_id <> '') NOT VALID,
    ADD CONSTRAINT pr_records_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE SET NULL NOT VALID;
COMMENT ON COLUMN pr_records.org_id IS
    'The organization (control-plane organizations.id) of the project that enrolled the repository. NULL: not enrolled, or written before enrolment.';
COMMENT ON COLUMN pr_records.project_id IS
    'The project that enrolled the repository (pipeline_repos.project_id). NULL: not enrolled, written before enrolment, or the project was deleted.';

ALTER TABLE pr_record_heads
    ADD COLUMN org_id text,
    ADD COLUMN project_id text,
    ADD CONSTRAINT pr_record_heads_org_id_not_blank CHECK (org_id <> '') NOT VALID,
    ADD CONSTRAINT pr_record_heads_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE SET NULL NOT VALID;
COMMENT ON COLUMN pr_record_heads.org_id IS 'Its record''s org_id.';
COMMENT ON COLUMN pr_record_heads.project_id IS 'Its record''s project_id.';

ALTER TABLE pr_record_reviews
    ADD COLUMN org_id text,
    ADD COLUMN project_id text,
    ADD CONSTRAINT pr_record_reviews_org_id_not_blank CHECK (org_id <> '') NOT VALID,
    ADD CONSTRAINT pr_record_reviews_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE SET NULL NOT VALID;
COMMENT ON COLUMN pr_record_reviews.org_id IS 'Its record''s org_id.';
COMMENT ON COLUMN pr_record_reviews.project_id IS 'Its record''s project_id.';

ALTER TABLE pr_record_checks
    ADD COLUMN org_id text,
    ADD COLUMN project_id text,
    ADD CONSTRAINT pr_record_checks_org_id_not_blank CHECK (org_id <> '') NOT VALID,
    ADD CONSTRAINT pr_record_checks_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE SET NULL NOT VALID;
COMMENT ON COLUMN pr_record_checks.org_id IS
    'The org_id of the project that enrolled the check''s repository. NULL: not enrolled, or written before enrolment.';
COMMENT ON COLUMN pr_record_checks.project_id IS
    'The project that enrolled the check''s repository. NULL: not enrolled, written before enrolment, or the project was deleted.';

ALTER TABLE pr_record_deliveries
    ADD COLUMN org_id text,
    ADD COLUMN project_id text,
    ADD CONSTRAINT pr_record_deliveries_org_id_not_blank CHECK (org_id <> '') NOT VALID,
    ADD CONSTRAINT pr_record_deliveries_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE SET NULL NOT VALID;
COMMENT ON COLUMN pr_record_deliveries.org_id IS
    'The org_id of the project that enrolled the delivery''s repository. NULL: not enrolled, or written before enrolment.';
COMMENT ON COLUMN pr_record_deliveries.project_id IS
    'The project that enrolled the delivery''s repository. NULL: not enrolled, written before enrolment, or the project was deleted.';
