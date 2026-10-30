-- forge:backward-compatible — adds one nullable column to workflow_catalog_snapshots; the previous release never reads it and its inserts leave it NULL.

-- The git state the pinned machine's files were synced from (the
-- config.SyncedRepoState JSON array of project_machine_catalogs.repo_states_json),
-- read in the same row read as the catalog it pins. A firing that resolves a
-- definition in the snapshot names the machine, branch and commit of that
-- snapshot, not of whatever the machine pushed after. NULL for a union
-- snapshot (no one machine) or a machine that reported no git state.
ALTER TABLE workflow_catalog_snapshots ADD COLUMN repo_states_json text;
