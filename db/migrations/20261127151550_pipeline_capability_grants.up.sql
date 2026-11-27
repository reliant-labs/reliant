-- forge:backward-compatible — a new table and two new nullable columns on pr_head_verdicts; the previous release never reads the table, and it inserts verdicts by named columns, leaving the new ones NULL.

-- Migration: pipeline_capability_grants
-- Created: 2026-11-27T15:15:50Z
--
-- The owner's grants of a pipeline capability (approve: a model review
-- verdict counts; land: the merge queue may squash-merge what such a verdict
-- admitted) to one workflow activation (a triggers row), on one machine pool
-- (internal/pipeline/grants). A run holds the capability only while it is a
-- run of that activation, executing on a member of that pool, and the grant
-- is live. Grants are made by approving a card in chat, which records the
-- approval and the owner who approved it; revoking sets revoked_at and never
-- deletes the row, so the table is its own audit trail. A grant goes with its
-- activation, its pool or its repository. Inert: nothing writes the table
-- until the pipeline ships behind `pipeline.enabled`.
--
-- Rollback: none needed. The previous release never reads the table or the
-- new pr_head_verdicts columns.
CREATE TABLE pipeline_capability_grants (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    -- The owner who granted it: the owner of both the activation and the
    -- pool, checked when it is written and again on every use.
    user_id TEXT NOT NULL CHECK (user_id <> ''),
    -- The activation's organization; NULL when it has none.
    org_id TEXT,
    trigger_id TEXT NOT NULL REFERENCES triggers (id) ON DELETE CASCADE,
    machine_pool_id TEXT NOT NULL REFERENCES machine_pools (id) ON DELETE CASCADE,
    -- The enrolled repository it is limited to; NULL for every repository the
    -- activation's runs act on.
    repo_id TEXT REFERENCES pipeline_repos (id) ON DELETE CASCADE,
    capability TEXT NOT NULL,
    -- The approval card whose approval made the grant.
    approval_id TEXT,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    revoked_by_user_id TEXT,
    CONSTRAINT pipeline_capability_grants_capability CHECK (capability IN ('approve', 'land')),
    CONSTRAINT pipeline_capability_grants_revoked_by CHECK ((revoked_at IS NULL) = (revoked_by_user_id IS NULL))
);

COMMENT ON TABLE pipeline_capability_grants IS 'forge:no-entity';
COMMENT ON CONSTRAINT pipeline_capability_grants_capability ON pipeline_capability_grants IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineCapabilities';

-- At most one live grant of a capability per activation, pool and repository
-- scope: granting again is a no-op. It also serves Authorize's lookup by
-- activation.
CREATE UNIQUE INDEX pipeline_capability_grants_live
    ON pipeline_capability_grants (trigger_id, machine_pool_id, capability, (COALESCE(repo_id, '')))
    WHERE revoked_at IS NULL;
CREATE INDEX pipeline_capability_grants_user_idx ON pipeline_capability_grants (user_id, granted_at DESC);
CREATE INDEX pipeline_capability_grants_pool_idx ON pipeline_capability_grants (machine_pool_id);
CREATE INDEX pipeline_capability_grants_repo_idx ON pipeline_capability_grants (repo_id) WHERE repo_id IS NOT NULL;

-- Which activation's run, on which machine, wrote a model verdict: the land
-- check finds the activation whose run approved a head from them, so a grant
-- revoked after the verdict still stops the landing. NULL on a structural
-- verdict and on every row written before these columns.
ALTER TABLE pr_head_verdicts
    ADD COLUMN trigger_id TEXT,
    ADD COLUMN daemon_id TEXT;
