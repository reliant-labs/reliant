-- forge:backward-compatible — a new table and a new nullable column; the previous release never reads or writes either, and its runs load their children live as they always have.

-- The workflows and presets a run started with: the project's, and the
-- owner's account workflows a name can resolve to first. A run's root load
-- stores the catalog it resolved against (its machine's, or the union) here,
-- keyed by the content's hash, and every later load in the run (children,
-- presets, loop bodies, after a worker restart) reads this row instead of the
-- live catalog: a run that lasts an hour cannot mix a parent from one push
-- with children from a later one, and what it executed has a recorded answer.
-- Identical catalogs share a row. A row not used for 90 days is deleted (the
-- store prunes on write); a run that resumes after that fails closed, naming
-- the pin.
CREATE TABLE workflow_catalog_snapshots (
    -- sha256 (hex) of the snapshot's content: workflows_json, presets_json,
    -- user_workflows_json.
    id text PRIMARY KEY,
    project_id text NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The machine whose catalog it is, NULL for the union of every machine's.
    daemon_id text,
    -- config.StoredWorkflow / config.StoredPreset JSON arrays, as synced.
    workflows_json text,
    presets_json text,
    -- The owner's account workflows as a ref resolved them at the pin, by
    -- slug (db.PinnedUserWorkflow): a run's later loads resolve a name the
    -- same way even after the owner saves, creates or finishes a workflow.
    user_workflows_json text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX workflow_catalog_snapshots_last_used ON workflow_catalog_snapshots (last_used_at);
CREATE INDEX workflow_catalog_snapshots_project ON workflow_catalog_snapshots (project_id);

COMMENT ON TABLE workflow_catalog_snapshots IS 'forge:no-entity';

-- The catalog the run at this position started with, so a restart from the
-- checkpoint (a ghost or failed run's recovery) loads the definitions the
-- interrupted run was executing, not whatever was pushed since. NULL for a
-- run with no pin.
ALTER TABLE workflow_checkpoints ADD COLUMN catalog_pin text;
COMMENT ON COLUMN workflow_checkpoints.catalog_pin IS 'forge:readable a content hash naming a stored workflow catalog; not a credential';
