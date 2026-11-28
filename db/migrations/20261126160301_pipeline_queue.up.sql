-- forge:backward-compatible — new tables nothing older reads

-- Migration: pipeline_queue
-- Created: 2026-11-26T16:03:01Z
--
-- The Reliant merge queue's state (pipeline redesign §3.2, §3.3, §4.4; build
-- plan task R4): the entries waiting to land (queue_entries), the candidate
-- batches the gate judges (queue_batches) and every verdict on a main sha
-- (main_health). Inert: nothing reads or writes these tables until the
-- pipeline ships behind `pipeline.enabled`. Only the queue service writes
-- them (§4.6).
--
-- Tenancy as on every pipeline table (20261125211730_pipeline_repos_and_settings).
-- Each closed vocabulary is a named CHECK that declares the Go set it mirrors
-- (internal/db/core/pipeline_queue.go).
--
-- The tables are new and empty, so their indexes are built here, in the
-- transaction that creates them.
--
-- Post-deploy verification (prod): the three tables exist and are empty:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pipeline queue tables" \
--     "SELECT (SELECT count(*) FROM queue_entries), (SELECT count(*) FROM queue_batches), (SELECT count(*) FROM main_health)"
--   → 0, 0, 0
--
-- Rollback: none needed. The previous release never names these tables; they
-- stay in place, unread.

-- One candidate a gate judges: main at base_sha plus its entries squashed on
-- in order, or (kind verify) main's own tree.
CREATE TABLE queue_batches (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES pipeline_repos (id) ON DELETE CASCADE,
    -- Main's sha the candidate was built on.
    base_sha TEXT NOT NULL CHECK (base_sha <> ''),
    -- The candidate's sha and the ref it was pushed to
    -- (refs/heads/reliant-queue/<batch-id>); null while building.
    candidate_sha TEXT,
    candidate_ref TEXT,
    -- The batch a bisect half (or a rerun) was split from. Not SET NULL:
    -- that would break queue_batches_bisect_parent mid-cascade. Batches are
    -- history, deleted only with their repository or project.
    parent_batch_id TEXT REFERENCES queue_batches (id),
    kind TEXT NOT NULL DEFAULT 'normal',
    -- [T_1..T_n]: the tree after squashing entries 1..i onto base_sha,
    -- checked against main after each squash merge (§3.2 step 5).
    expected_trees_json JSONB,
    -- Main's sha after the batch's last merge: the only sha of the batch
    -- releases and main_health use. Set exactly when the batch landed.
    landed_sha TEXT,
    -- Where the gate ran: the boundary it ran in, and the machine.
    isolation_backend TEXT,
    gate_machine_id TEXT,
    state TEXT NOT NULL DEFAULT 'building',
    -- How the gate was dispatched: an engine run on a gate machine, named
    -- GitHub check runs on the candidate, or both (every part must pass).
    gate_runner TEXT,
    gate_run_id TEXT,
    check_names TEXT[] NOT NULL DEFAULT '{}',
    attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt >= 1),
    -- Failing steps and tests, infra matches.
    verdict_json JSONB,
    -- The gate's per-stage results (lint → test → build → sbom_scan, owner
    -- D13): gate-result.json's per-stage section, keyed by stage, each with
    -- its verdict (pass, fail, unproven, warn or off). Null until gated, or
    -- for a gate that reported no stages.
    stages_json JSONB,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT queue_batches_kind CHECK (kind IN (
        'normal', 'bisect', 'rerun', 'revert', 'verify', 'emergency', 'emergency_verify')),
    CONSTRAINT queue_batches_state CHECK (state IN (
        'building', 'gating', 'passed', 'failed', 'unproven', 'landed', 'abandoned')),
    CONSTRAINT queue_batches_isolation_backend CHECK (isolation_backend IN ('vm', 'sysbox', 'checks')),
    CONSTRAINT queue_batches_gate_runner CHECK (gate_runner IN ('machine', 'checks', 'both')),
    CONSTRAINT queue_batches_landed CHECK ((state = 'landed') = (landed_sha IS NOT NULL AND landed_sha <> '')),
    CONSTRAINT queue_batches_bisect_parent CHECK (kind <> 'bisect' OR parent_batch_id IS NOT NULL),
    CONSTRAINT queue_batches_parent_not_self CHECK (parent_batch_id <> id),
    CONSTRAINT queue_batches_stages_object CHECK (jsonb_typeof(stages_json) = 'object'),
    CONSTRAINT queue_batches_expected_trees_array CHECK (jsonb_typeof(expected_trees_json) = 'array')
);

COMMENT ON TABLE queue_batches IS 'forge:no-entity';
COMMENT ON CONSTRAINT queue_batches_kind ON queue_batches IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueBatchKinds';
COMMENT ON CONSTRAINT queue_batches_state ON queue_batches IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueBatchStates';
COMMENT ON CONSTRAINT queue_batches_isolation_backend ON queue_batches IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueIsolationBackends';
COMMENT ON CONSTRAINT queue_batches_gate_runner ON queue_batches IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueGateRunners';
CREATE INDEX queue_batches_repo_idx ON queue_batches (repo_id, state, started_at);
CREATE INDEX queue_batches_parent_idx ON queue_batches (parent_batch_id) WHERE parent_batch_id IS NOT NULL;
CREATE INDEX queue_batches_project_idx ON queue_batches (project_id);

-- One approved PR head in the queue. A PR is in the queue (waiting or
-- batched) at most once.
CREATE TABLE queue_entries (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES pipeline_repos (id) ON DELETE CASCADE,
    record_id TEXT NOT NULL REFERENCES pr_records (id) ON DELETE CASCADE,
    -- The reviewed head that lands: the squash merge binds to it.
    head_sha TEXT NOT NULL CHECK (head_sha <> ''),
    priority INTEGER NOT NULL,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The batch the entry is (or last was) in. Not SET NULL: that would
    -- break queue_entries_batched mid-cascade. A batch is history, deleted
    -- only with its repository or project, alongside its entries.
    batch_id TEXT REFERENCES queue_batches (id),
    state TEXT NOT NULL DEFAULT 'waiting',
    -- Why it left the queue: set exactly when the state is ejected.
    eject_reason TEXT,
    -- Main's sha after its squash merge, and when: set exactly when landed.
    landed_sha TEXT,
    landed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT queue_entries_state CHECK (state IN ('waiting', 'batched', 'landed', 'ejected', 'removed')),
    CONSTRAINT queue_entries_eject_reason
        CHECK (eject_reason IN ('conflict', 'gate_red', 'head_moved', 'hold', 'closed')),
    CONSTRAINT queue_entries_priority_range CHECK (priority BETWEEN 0 AND 100),
    CONSTRAINT queue_entries_ejected CHECK ((state = 'ejected') = (eject_reason IS NOT NULL)),
    CONSTRAINT queue_entries_landed CHECK (
        (state = 'landed') = (landed_sha IS NOT NULL AND landed_sha <> '')
        AND (landed_sha IS NULL) = (landed_at IS NULL)),
    CONSTRAINT queue_entries_batched CHECK (state <> 'batched' OR batch_id IS NOT NULL)
);

COMMENT ON TABLE queue_entries IS 'forge:no-entity';
COMMENT ON CONSTRAINT queue_entries_state ON queue_entries IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueEntryStates';
COMMENT ON CONSTRAINT queue_entries_eject_reason ON queue_entries IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueEjectReasons';
-- A PR is waiting or batched once: a second enqueue of it is refused.
CREATE UNIQUE INDEX queue_entries_active_record_key ON queue_entries (record_id)
    WHERE state IN ('waiting', 'batched');
-- The pick: a repository's waiting entries by priority DESC, enqueued_at.
CREATE INDEX queue_entries_pick_idx ON queue_entries (repo_id, priority DESC, enqueued_at)
    WHERE state = 'waiting';
CREATE INDEX queue_entries_batch_idx ON queue_entries (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX queue_entries_project_idx ON queue_entries (project_id);

-- Every verdict on a sha of main: a gate's pass on a batch tip, a recheck, a
-- release, a nightly. A daily % red and % unproven per repository is one
-- query. Rows are only inserted.
CREATE TABLE main_health (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES pipeline_repos (id) ON DELETE CASCADE,
    sha TEXT NOT NULL CHECK (sha <> ''),
    source TEXT NOT NULL,
    verdict TEXT NOT NULL,
    -- The failure signature, for a fail or an unproven.
    signature TEXT,
    -- The batch (source gate) or the run (recheck, release, nightly) that
    -- judged it.
    batch_id TEXT REFERENCES queue_batches (id) ON DELETE SET NULL,
    run_id TEXT,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT main_health_source CHECK (source IN ('gate', 'recheck', 'release', 'nightly')),
    CONSTRAINT main_health_verdict CHECK (verdict IN ('pass', 'fail', 'unproven'))
);

COMMENT ON TABLE main_health IS 'forge:no-entity';
COMMENT ON CONSTRAINT main_health_source ON main_health IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllMainHealthSources';
COMMENT ON CONSTRAINT main_health_verdict ON main_health IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllMainHealthVerdicts';
-- A sha's verdicts (release.cut: every component sha has a pass), and a
-- repository's history, newest first.
CREATE INDEX main_health_sha_idx ON main_health (repo_id, sha);
CREATE INDEX main_health_observed_idx ON main_health (repo_id, observed_at DESC);
CREATE INDEX main_health_project_idx ON main_health (project_id);
