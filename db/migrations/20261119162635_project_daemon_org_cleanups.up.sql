-- forge:backward-compatible — adds a new table no statement of the previous release names.

-- Migration: project_daemon_org_cleanups
-- Created: 2026-11-19T16:26:32Z
--
-- The audit of the machine-org cleanup: every checkout the connect reconcile
-- detaches from a machine because its project is in another org than the
-- machine's (internal/handlers/tools_daemon, detachOtherOrgCheckouts). The
-- cleanup detaches as a user's detach does (db.Repo.DetachProjectDaemonOfOtherOrg):
-- the project_daemons row and the machine's synced config of the project
-- (project_machine_catalogs). Both are kept whole here (detached_row and
-- detached_catalog, to_jsonb of each row as it was), in the same transaction
-- as their delete, so the detach can be undone exactly:
-- dev-docs/machine-org-rollout.md restores both with jsonb_populate_record and
-- stamps restored_at.
--
-- Nothing reads it in the product: it is for the post-deploy check and the
-- recovery procedure. project_id and daemon_id carry no foreign keys, so the
-- record outlives a project or machine deleted later.
CREATE TABLE project_daemon_org_cleanups (
    id bigserial PRIMARY KEY,
    project_id text NOT NULL,
    daemon_id text NOT NULL,
    user_id text NOT NULL,
    path text NOT NULL,
    -- The machine's org that ruled the row out, and the project's org then.
    machine_org_id text NOT NULL,
    project_org_id text,
    -- The detached project_daemons row, whole.
    detached_row jsonb NOT NULL,
    -- The machine's project_machine_catalogs row of the project, whole: its
    -- synced workflows, presets, skills, memories and config, which the
    -- project's config union is rewritten without. NULL when the machine had
    -- synced none.
    detached_catalog jsonb,
    detached_at timestamptz NOT NULL DEFAULT now(),
    -- Set by the recovery procedure when the row was put back.
    restored_at timestamptz
);
-- No entity: written and read by raw SQL only (store.RecordProjectDaemonOrgCleanup).
COMMENT ON TABLE project_daemon_org_cleanups IS 'forge:no-entity';
CREATE INDEX project_daemon_org_cleanups_detached_at ON project_daemon_org_cleanups (detached_at);
