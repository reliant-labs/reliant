-- forge:backward-compatible — a new table; the previous release never reads it and keeps writing project_configs.project_workflows_json/project_presets_json, which stay the union this release derives.

-- Each machine's own synced .reliant/workflows and .reliant/presets for a
-- project. A machine's snapshot replaces ITS row only, so one machine that
-- lacks a checkout can no longer erase what another machine provides.
-- project_configs.project_workflows_json and project_presets_json become the
-- union of these rows (per file, newest push wins), which every listing and
-- chat run keeps reading; a trigger resolves its workflow against its target
-- machine's row, because that is what its runs execute against.
CREATE TABLE project_machine_catalogs (
    project_id text NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    daemon_id text NOT NULL REFERENCES daemons (id) ON DELETE CASCADE,
    -- config.StoredWorkflow JSON array; NULL when the machine synced none.
    workflows_json text,
    -- config.StoredPreset JSON array; NULL when the machine synced none.
    presets_json text,
    -- Files this machine once provided and no longer does, with the path
    -- they were synced from (config.RemovedCatalogFile JSON array), so a
    -- miss can name where the definition used to live.
    removed_json text,
    pushed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, daemon_id)
);

CREATE INDEX project_machine_catalogs_daemon ON project_machine_catalogs (daemon_id);

COMMENT ON TABLE project_machine_catalogs IS 'forge:no-entity';

-- Seed each project's last pusher, the only provenance the old row kept; every
-- other machine fills its own row on its next connect (discovery always asks
-- for a full snapshot).
INSERT INTO project_machine_catalogs (project_id, daemon_id, workflows_json, presets_json, pushed_at, updated_at)
SELECT pc.project_id, pc.daemon_id, pc.project_workflows_json, pc.project_presets_json, pc.pushed_at, pc.updated_at
FROM project_configs pc
JOIN projects p ON p.id = pc.project_id
JOIN daemons d ON d.id = pc.daemon_id
ON CONFLICT DO NOTHING;
