-- forge:backward-compatible — adds one nullable column to project_machine_catalogs; the previous release never reads it and its writes leave it NULL.

-- The git state each machine's synced .reliant files were read from: per
-- repo (the project root's, "" , and each nested repo's that has a .reliant
-- directory), its branch, HEAD commit, whether .reliant had uncommitted
-- changes, and origin's default branch (config.SyncedRepoState JSON array).
-- NULL until the machine's daemon reports it. It is what an automation's
-- definition is shown with ("@ main 1a2b3c4, clean"), and what warns when an
-- automation's source is on a feature branch or an uncommitted edit.
ALTER TABLE project_machine_catalogs ADD COLUMN repo_states_json text;
