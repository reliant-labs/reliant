-- forge:backward-compatible — widens two CHECKs of pipeline_setting_changes, adds nullable columns and relaxes changed_by_user_id's NOT NULL for config changes only; every row the previous release could write (it writes none) still satisfies every constraint.

-- Migration: pipeline_setting_changes_gate_stages
-- Created: 2026-11-26T16:42:36Z
--
-- A gate stage turned off or back on is a recorded setting change (owner
-- D13; pipeline redesign §3.3; build plan task R6d). The stage is turned in
-- the repository's .reliant/pipeline.yaml, so the change is recorded when
-- the PR that changed it lands on base_branch (R8 writes the row,
-- changed_via = 'config'), not by an admin in the app. Inert: nothing writes
-- this table until the pipeline ships behind `pipeline.enabled`.
--
--   - setting gains gate.stage.lint, gate.stage.test, gate.stage.build and
--     gate.stage.sbom_scan; changed_via gains config. Both stay closed sets
--     mirrored in internal/db/core/pipeline.go.
--   - pipeline_setting_changes_config: a gate stage changes only through
--     config, and config changes only a gate stage. A project setting stays
--     writable only by a signed-in admin (web, desktop).
--   - The actor of a config change is the merging PR's author, who may have
--     no Reliant user. So changed_by_user_id becomes nullable, and
--     pipeline_setting_changes_actor keeps it required for every
--     web/desktop change; a config change names its provider actor instead
--     (changed_by_provider, _provider_user_id, _login), plus the Reliant user
--     when R8 can map one. It also names where the change came from: the
--     repository, the base_branch sha that carried it (config_sha) and the PR
--     (config_record_id).
--
-- The CHECKs are dropped and re-added in one ALTER TABLE: one lock and no
-- instant without them. They are added valid, not NOT VALID: a widening
-- never invalidates a row, and the new cross-column CHECKs hold on every
-- existing row, since nothing has written one (the table is unwritten until
-- the pipeline ships), so the validation scan has nothing to read and the
-- ACCESS EXCLUSIVE lock covers the catalog change alone. postgres drops a
-- constraint's comment with it, so the closed-set declarations are re-issued.
-- lock_timeout bounds the wait.
--
-- Post-deploy verification (prod): the widened CHECKs are installed and
-- validated (true, 4):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pipeline setting changes widened" \
--     "SELECT bool_and(convalidated), count(*) FROM pg_constraint WHERE conname IN ('pipeline_setting_changes_setting', 'pipeline_setting_changes_changed_via', 'pipeline_setting_changes_config', 'pipeline_setting_changes_actor')"
--
-- Rollback: none needed. The previous release never names this table.
SET LOCAL lock_timeout = '5s';

ALTER TABLE pipeline_setting_changes
    -- The repository a config change is about (pipeline.yaml is per
    -- repository). Kept when the repository is unenrolled: the history
    -- outlives the enrolment.
    ADD COLUMN repo_id TEXT REFERENCES pipeline_repos (id) ON DELETE SET NULL,
    -- The base_branch commit that carried the config change, and the PR
    -- that landed it.
    ADD COLUMN config_sha TEXT,
    ADD COLUMN config_record_id TEXT REFERENCES pr_records (id) ON DELETE SET NULL,
    -- The merging PR's author at the provider: the actor of a config change.
    ADD COLUMN changed_by_provider TEXT,
    ADD COLUMN changed_by_provider_user_id TEXT,
    ADD COLUMN changed_by_login TEXT,
    ALTER COLUMN changed_by_user_id DROP NOT NULL,
    DROP CONSTRAINT pipeline_setting_changes_setting,
    ADD CONSTRAINT pipeline_setting_changes_setting CHECK (setting IN (
        'emergency_land_policy', 'emergency_land_daily_cap', 'release_approval',
        'gate.stage.lint', 'gate.stage.test', 'gate.stage.build', 'gate.stage.sbom_scan')),
    DROP CONSTRAINT pipeline_setting_changes_changed_via,
    ADD CONSTRAINT pipeline_setting_changes_changed_via CHECK (changed_via IN ('web', 'desktop', 'config')),
    ADD CONSTRAINT pipeline_setting_changes_config CHECK (
        (changed_via = 'config')
        = (setting IN ('gate.stage.lint', 'gate.stage.test', 'gate.stage.build', 'gate.stage.sbom_scan'))),
    ADD CONSTRAINT pipeline_setting_changes_actor CHECK (CASE
        WHEN changed_via = 'config' THEN
            config_sha IS NOT NULL AND config_sha <> ''
            AND changed_by_provider IS NOT NULL AND changed_by_provider <> ''
            AND changed_by_provider_user_id IS NOT NULL AND changed_by_provider_user_id <> ''
            AND changed_by_login IS NOT NULL AND changed_by_login <> ''
        ELSE changed_by_user_id IS NOT NULL
            AND config_sha IS NULL AND config_record_id IS NULL
            AND changed_by_provider IS NULL AND changed_by_provider_user_id IS NULL AND changed_by_login IS NULL
        END);

COMMENT ON CONSTRAINT pipeline_setting_changes_setting ON pipeline_setting_changes IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineSettings';
COMMENT ON CONSTRAINT pipeline_setting_changes_changed_via ON pipeline_setting_changes IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineChangedVia';
COMMENT ON COLUMN pipeline_setting_changes.changed_by_user_id IS
    'The Reliant user who made the change. Required for web and desktop; for config, set only when the PR author maps to a Reliant user.';
