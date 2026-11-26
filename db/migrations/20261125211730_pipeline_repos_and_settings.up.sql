-- forge:backward-compatible — new tables nothing older reads

-- Migration: pipeline_repos_and_settings
-- Created: 2026-11-25T21:17:30Z
--
-- The PR and release pipeline's enrolment and per-project settings (pipeline
-- redesign §4.1, §4.3; build plan task R1). Inert: nothing reads or writes
-- these tables until the pipeline ships behind `pipeline.enabled`.
--
-- Tenancy: every row carries org_id and project_id. project_id references
-- projects and goes with the project; org_id is control-plane's, so it has no
-- foreign key (as on every other org-scoped table).
--
-- Each closed vocabulary is a named CHECK that declares the Go set it mirrors
-- (internal/db/core/pipeline.go), so `forge lint --closed-sets` fails the
-- moment the two drift.
--
-- The tables are new and empty, so their indexes are built here, in the
-- transaction that creates them, rather than CONCURRENTLY in files of their
-- own.
--
-- Post-deploy verification (prod): the three tables exist and are empty:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pipeline tables" \
--     "SELECT (SELECT count(*) FROM pipeline_repos), (SELECT count(*) FROM pipeline_project_settings), (SELECT count(*) FROM pipeline_setting_changes)"
--
-- Rollback: none needed. The previous release never names these tables; they
-- stay in place, unread.

-- One row per enrolled repository. The provider keys are pr_records'.
CREATE TABLE pipeline_repos (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    -- The installation (GitHub App) the repository's deliveries come through.
    account_key TEXT NOT NULL,
    -- The provider's id of the repository, stable across renames.
    repository_id TEXT NOT NULL,
    -- owner/name as last observed, for lookup and display.
    repository TEXT NOT NULL,
    -- The branch PRs merge into, and the only ref config is read from.
    base_branch TEXT NOT NULL DEFAULT 'main' CHECK (base_branch <> ''),
    -- off: enrolled, nothing acts. shadow: decisions are computed and
    -- recorded, never acted on. on: the pipeline acts.
    mode TEXT NOT NULL DEFAULT 'off',
    -- The .reliant/pipeline.yaml read from base_branch at config_sha, as
    -- validated. Null until it has been read. Never read from a PR head.
    config_sha TEXT,
    config_json JSONB,
    queue_state TEXT NOT NULL DEFAULT 'running',
    -- The pipeline_decisions row that paused the queue. That table arrives
    -- in a later migration, which adds the foreign key.
    queue_paused_by_decision_id TEXT,
    created_by_user_id TEXT NOT NULL CHECK (created_by_user_id <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Compare-and-set: every state transition is UPDATE … WHERE version = $v.
    version BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT pipeline_repos_mode CHECK (mode IN ('off', 'shadow', 'on')),
    CONSTRAINT pipeline_repos_queue_state CHECK (queue_state IN ('running', 'paused', 'degraded')),
    CONSTRAINT pipeline_repos_provider_repository_key UNIQUE (provider, repository_id)
);

COMMENT ON TABLE pipeline_repos IS 'forge:no-entity';
COMMENT ON CONSTRAINT pipeline_repos_mode ON pipeline_repos IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineRepoModes';
COMMENT ON CONSTRAINT pipeline_repos_queue_state ON pipeline_repos IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineQueueStates';
CREATE INDEX pipeline_repos_project_idx ON pipeline_repos (project_id);

-- One row per project. Written only by org admins from a browser session;
-- every write inserts its pipeline_setting_changes row in the same
-- transaction. A project with no row has the defaults.
CREATE TABLE pipeline_project_settings (
    project_id TEXT PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    emergency_land_policy TEXT NOT NULL DEFAULT 'ask',
    emergency_land_daily_cap INTEGER NOT NULL DEFAULT 3 CHECK (emergency_land_daily_cap >= 0),
    release_approval TEXT NOT NULL DEFAULT 'prod_only',
    updated_by_user_id TEXT NOT NULL CHECK (updated_by_user_id <> ''),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Compare-and-set: every write is UPDATE … WHERE version = $v.
    version BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT pipeline_project_settings_emergency_land_policy
        CHECK (emergency_land_policy IN ('ask', 'auto_allow', 'off')),
    CONSTRAINT pipeline_project_settings_release_approval
        CHECK (release_approval IN ('none', 'per_env', 'prod_only'))
);

COMMENT ON TABLE pipeline_project_settings IS 'forge:no-entity';
COMMENT ON CONSTRAINT pipeline_project_settings_emergency_land_policy ON pipeline_project_settings IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineEmergencyLandPolicies';
COMMENT ON CONSTRAINT pipeline_project_settings_release_approval ON pipeline_project_settings IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineReleaseApprovals';

-- Append-only audit of every settings write: rows are inserted, never
-- updated. A decision the policy approved names the row that was in force
-- (pipeline_decisions.policy_change_id, a later migration).
CREATE TABLE pipeline_setting_changes (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The pipeline_project_settings column that changed.
    setting TEXT NOT NULL,
    -- The value before the write, as text; null when the project had no
    -- settings row (the default was in force).
    old_value TEXT,
    new_value TEXT NOT NULL,
    changed_by_user_id TEXT NOT NULL CHECK (changed_by_user_id <> ''),
    changed_via TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pipeline_setting_changes_setting
        CHECK (setting IN ('emergency_land_policy', 'emergency_land_daily_cap', 'release_approval')),
    CONSTRAINT pipeline_setting_changes_changed_via CHECK (changed_via IN ('web', 'desktop'))
);

COMMENT ON TABLE pipeline_setting_changes IS 'forge:no-entity';
COMMENT ON CONSTRAINT pipeline_setting_changes_setting ON pipeline_setting_changes IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineSettings';
COMMENT ON CONSTRAINT pipeline_setting_changes_changed_via ON pipeline_setting_changes IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineChangedVia';
-- A project's settings history, newest first.
CREATE INDEX pipeline_setting_changes_project_idx ON pipeline_setting_changes (project_id, created_at DESC);
