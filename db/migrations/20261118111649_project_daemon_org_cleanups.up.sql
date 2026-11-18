-- forge:backward-compatible — adds a new table no statement of the previous release names.

-- Migration: project_daemon_org_cleanups
-- Created: 2026-11-18T11:16:47Z
--
-- The audit of the machine-org cleanup: every project_daemons row the connect
-- reconcile detaches because its project is in another org than the
-- machine's (internal/handlers/tools_daemon, detachOtherOrgCheckouts). The
-- whole row is kept (detached_row, to_jsonb of the project_daemons row as it was), so
-- the detach can be undone exactly: dev-docs/machine-org-rollout.md
-- restores rows from here with jsonb_populate_record and stamps restored_at.
-- A detach and its audit row are one statement (a DELETE … RETURNING feeding
-- this INSERT), so neither exists without the other.
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
    detached_at timestamptz NOT NULL DEFAULT now(),
    -- Set by the recovery procedure when the row was put back.
    restored_at timestamptz
);
-- No entity: written and read by raw SQL only (store.DetachProjectDaemonOfOtherOrg).
COMMENT ON TABLE project_daemon_org_cleanups IS 'forge:no-entity';
CREATE INDEX project_daemon_org_cleanups_detached_at ON project_daemon_org_cleanups (detached_at);
